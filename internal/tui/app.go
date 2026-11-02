package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/thezim/aicm/internal/awsconfig"
	"github.com/thezim/aicm/internal/config"
	"github.com/thezim/aicm/internal/daemon"
	"github.com/thezim/aicm/internal/display"
	"github.com/thezim/aicm/internal/ec2"
	"github.com/thezim/aicm/internal/editor"
	"github.com/thezim/aicm/internal/ipc"
	"github.com/thezim/aicm/internal/model"
	"github.com/thezim/aicm/internal/tunnel"
)

type viewMode int

const (
	viewInstances viewMode = iota
	viewConnections
	viewInstanceInfo
	viewHelp
	viewError
)

// Options are startup flags for the TUI.
type Options struct {
	ConfigPath string
	Profile    string
	Region     string
	Version    string
}

type App struct {
	ctx  context.Context
	opts Options

	width  int
	height int

	loading   bool
	fatal     string
	fatalHint string

	cfg        *config.Config
	configPath string
	session    *awsconfig.Session
	accountID  string
	region     string
	profile    string

	theme     model.Colors
	rdpOpts   model.RDPOptions
	opener    []string
	editor    []string
	quitAfter bool
	states    []string

	instances    []ec2.Instance
	named        []display.Named
	visibleNamed []display.Named
	nameByID     map[string]display.Named
	selected     int
	offset       int
	infoOffset   int
	view         viewMode
	prevView     viewMode

	filterQueries [2]string
	filterInput   []rune
	filterEditing bool
	filterPrev    string
	filterView    viewMode

	client    *ipc.Client
	info      model.DaemonInfo
	listening bool
	reconnect bool

	conns        map[string]model.Connection
	closedIDs    map[string]time.Time
	connOrder    []string
	visibleConns []string
	connSel      int
	connOffset   int
	confirmID    string

	confirmStopID  string
	pendingReady   bool
	actionPending  bool
	consolePending bool
	status         string
	statusErr      bool
	statusAt       time.Time
	spinner        int

	summary string
	quit    bool
}

// Run starts the TUI and returns a one-line summary when an RDP session was
// launched before exit.
func Run(ctx context.Context, opts Options) (string, error) {
	configPath := strings.TrimSpace(opts.ConfigPath)
	if configPath == "" {
		if path, err := config.DefaultPath(); err == nil {
			configPath = path
		}
	}
	if configPath != "" {
		configPath = config.ExpandPath(configPath)
	}
	app := &App{
		ctx:        ctx,
		opts:       opts,
		configPath: configPath,
		loading:    true,
		view:       viewInstances,
		conns:      make(map[string]model.Connection),
		closedIDs:  make(map[string]time.Time),
		nameByID:   make(map[string]display.Named),
		status:     "Resolving AWS configuration…",
	}
	program := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := program.Run()
	if app.client != nil {
		_ = app.client.Close()
	}
	if err != nil {
		return "", err
	}
	if m, ok := final.(*App); ok {
		return m.summary, nil
	}
	return "", nil
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(a.loadCmd(), a.startDaemonCmd(), tickCmd())
}

type loadedMsg struct {
	cfg       *config.Config
	session   *awsconfig.Session
	instances []ec2.Instance
	notice    string
	warnings  []string
}

type startupErrMsg struct {
	err  error
	hint string
}

type instancesMsg struct {
	instances []ec2.Instance
	note      string
}
type instancesErrMsg struct{ err error }

const (
	actionStart = "start"
	actionStop  = "stop"
)

type instanceActionResultMsg struct {
	action string
	id     string
	name   string
	err    error
}

type daemonMsg struct {
	client *ipc.Client
	info   model.DaemonInfo
	conns  []model.Connection
	err    error
}
type eventsClosedMsg struct{}
type reconnectMsg struct{}
type eventMsg struct{ ev ipc.Event }

type connectResultMsg struct {
	conn model.Connection
	err  error
}
type consoleReadyMsg struct {
	name string
	cmd  *exec.Cmd
	err  error
}
type consoleDoneMsg struct {
	name string
	err  error
}
type editorReadyMsg struct{ cmd *exec.Cmd }
type editorDoneMsg struct{ err error }
type reopenResultMsg struct {
	conn model.Connection
	err  error
}
type closeResultMsg struct{ err error }
type refreshConnsMsg struct {
	conns []model.Connection
	err   error
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a *App) loadCmd() tea.Cmd {
	ctx := a.ctx
	opts := a.opts
	path := a.configPath
	return func() tea.Msg {
		if strings.TrimSpace(path) == "" {
			var err error
			path, err = config.DefaultPath()
			if err != nil {
				return startupErrMsg{err: fmt.Errorf("resolve config path: %w", err), hint: "pass --config PATH"}
			}
		}
		cfg, err := config.Load(path)
		if err != nil {
			return startupErrMsg{err: err, hint: "Fix or remove the malformed config file. It was not modified."}
		}
		session, err := awsconfig.Resolve(ctx, opts.Profile, opts.Region)
		if err != nil {
			return startupErrMsg{err: err, hint: "Authenticate with AWS first (aws sso login / aws configure), or pass --profile and --region."}
		}
		ensure, err := config.EnsureAccount(ctx, cfg.Path, session.AccountID)
		if err != nil {
			return startupErrMsg{err: fmt.Errorf("update config: %w", err), hint: "Check that the config directory is writable."}
		}
		account := ensure.Account
		if _, ok := cfg.Account(session.AccountID); !ok {
			cfg.SetAccount(session.AccountID, account)
		}
		var warnings []string
		warnings = append(warnings, cfg.Warnings...)
		warnings = append(warnings, ensure.Warnings...)
		notice := ""
		if ensure.Added {
			notice = fmt.Sprintf("Added account %s to %s", session.AccountID, cfg.Path)
		}
		if ensure.WriteErr != nil {
			warnings = append(warnings, fmt.Sprintf("Could not save config %s: %v (using in-memory defaults)", cfg.Path, ensure.WriteErr))
		}
		client := ec2.NewClient(session.Config)
		instances, err := ec2.List(ctx, client, cfg.File.AWS.States)
		if err != nil {
			return startupErrMsg{err: err, hint: "EC2 requires ec2:DescribeInstances. Check credentials, region, and IAM permissions."}
		}
		return loadedMsg{
			cfg:       cfg,
			session:   session,
			instances: instances,
			notice:    notice,
			warnings:  warnings,
		}
	}
}

func (a *App) startDaemonCmd() tea.Cmd {
	ctx := a.ctx
	configPath := a.configPath
	return func() tea.Msg {
		client, err := daemon.EnsureRunning(ctx, configPath)
		if err != nil {
			return daemonMsg{err: err}
		}
		var info model.DaemonInfo
		if err := client.Call(ctx, "subscribe", nil, &info); err != nil {
			_ = client.Close()
			return daemonMsg{err: err}
		}
		var conns []model.Connection
		if err := client.Call(ctx, "list_connections", nil, &conns); err != nil {
			_ = client.Close()
			return daemonMsg{err: err}
		}
		return daemonMsg{client: client, info: info, conns: conns}
	}
}

func waitEvent(c *ipc.Client) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-c.Events()
		if !ok {
			return eventsClosedMsg{}
		}
		return eventMsg{ev: ev}
	}
}

func (a *App) editConfigCmd() tea.Cmd {
	path := a.configPath
	if path == "" {
		a.setStatus("Config path unavailable", true)
		return nil
	}
	configured := append([]string(nil), a.editor...)
	return func() tea.Msg {
		if err := config.EnsureFile(path); err != nil {
			return editorDoneMsg{err: fmt.Errorf("create config: %w", err)}
		}
		argv, err := editor.Resolve(configured, path)
		if err != nil {
			return editorDoneMsg{err: err}
		}
		return editorReadyMsg{cmd: exec.Command(argv[0], argv[1:]...)}
	}
}

func (a *App) reloadConfigAfterEdit(editErr error) tea.Cmd {
	latest, err := config.Load(a.configPath)
	if err != nil {
		if editErr != nil {
			a.setStatus(fmt.Sprintf("Editor failed: %v; config reload failed: %v", editErr, err), true)
		} else {
			a.setStatus("Config reload failed: "+err.Error(), true)
		}
		return nil
	}
	oldStates := append([]string(nil), a.states...)
	a.cfg = latest
	a.configPath = latest.Path
	a.theme = latest.EffectiveColors(a.accountID)
	a.rdpOpts = latest.AccountOrDefault(a.accountID).RDP
	a.opener = latest.File.App.RDPOpener
	a.editor = latest.File.App.Editor
	a.quitAfter = latest.File.App.QuitAfterRDP
	a.states = append([]string(nil), latest.File.AWS.States...)
	a.rebuildNames(a.selectedInstanceID())

	var cmd tea.Cmd
	note := "Configuration reloaded"
	if !slices.Equal(oldStates, a.states) && a.session != nil {
		note = "Configuration reloaded; refreshing instances…"
		cmd = a.refreshInstancesCmd(note)
	}
	switch {
	case editErr != nil:
		a.setStatus(fmt.Sprintf("Configuration reloaded, but the editor exited with an error: %v", editErr), true)
	case len(latest.Warnings) > 0:
		a.setStatus(strings.Join(latest.Warnings, " · "), true)
	default:
		a.setStatus(note, false)
	}
	return cmd
}

func (a *App) refreshInstancesCmd(note string) tea.Cmd {
	if a.session == nil {
		return nil
	}
	ctx := a.ctx
	session := a.session
	states := a.states
	return func() tea.Msg {
		instances, err := ec2.List(ctx, ec2.NewClient(session.Config), states)
		if err != nil {
			return instancesErrMsg{err: err}
		}
		return instancesMsg{instances: instances, note: note}
	}
}

func (a *App) refreshConnsCmd() tea.Cmd {
	if a.client == nil {
		return nil
	}
	client := a.client
	return func() tea.Msg {
		var conns []model.Connection
		err := client.Call(a.ctx, "list_connections", nil, &conns)
		return refreshConnsMsg{conns: conns, err: err}
	}
}

func (a *App) connectCmd() tea.Cmd {
	idx := a.selected
	if idx < 0 || idx >= len(a.visibleNamed) {
		return nil
	}
	named := a.visibleNamed[idx]
	inst := findInstance(a.instances, named.InstanceID)
	if !inst.IsWindows() {
		return func() tea.Msg {
			return connectResultMsg{err: fmt.Errorf("RDP is only supported for Windows instances")}
		}
	}
	if reason := instanceRunningMessage(named.Display, inst.State); reason != "" {
		return func() tea.Msg {
			return connectResultMsg{err: fmt.Errorf("%s", reason)}
		}
	}
	if a.session == nil || a.client == nil {
		return nil
	}
	client := a.client
	session := a.session
	accountID := a.accountID
	region := a.region
	rdpOpts := a.rdpOpts
	opener := a.opener
	configPath := ""
	if a.cfg != nil {
		configPath = a.cfg.Path
	}
	ctx := a.ctx
	return func() tea.Msg {
		// Pick up edits made while the TUI is open; fall back to the settings
		// loaded at startup if the file became unreadable.
		if configPath != "" {
			if latest, err := config.Load(configPath); err == nil {
				acct := latest.AccountOrDefault(accountID)
				rdpOpts = acct.RDP
				opener = latest.File.App.RDPOpener
			}
		}
		creds, err := session.Credentials(ctx)
		if err != nil {
			return connectResultMsg{err: err}
		}
		params := daemon.ConnectParams{
			AccountID:    accountID,
			InstanceID:   named.InstanceID,
			InstanceName: inst.Name,
			Platform:     inst.Platform,
			Region:       region,
			RDP:          rdpOpts,
			Opener:       opener,
			Credentials:  creds,
		}
		var conn model.Connection
		if err := client.Call(ctx, "connect", params, &conn); err != nil {
			return connectResultMsg{err: err}
		}
		return connectResultMsg{conn: conn}
	}
}

// prepareConsoleCmd resolves credentials and builds the foreground SSM shell
// command. The terminal is suspended when the returned command is started.
func (a *App) prepareConsoleCmd() tea.Cmd {
	if a.session == nil {
		return nil
	}
	idx := a.selected
	if idx < 0 || idx >= len(a.visibleNamed) {
		return nil
	}
	named := a.visibleNamed[idx]
	inst := findInstance(a.instances, named.InstanceID)
	if reason := instanceRunningMessage(named.Display, inst.State); reason != "" {
		return func() tea.Msg {
			return consoleReadyMsg{name: named.Display, err: fmt.Errorf("%s", reason)}
		}
	}
	session := a.session
	region := a.region
	ctx := a.ctx
	return func() tea.Msg {
		creds, err := session.Credentials(ctx)
		if err != nil {
			return consoleReadyMsg{name: named.Display, err: err}
		}
		args, err := tunnel.ShellCommand(tunnel.AWSBinary(), named.InstanceID, region)
		if err != nil {
			return consoleReadyMsg{name: named.Display, err: err}
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = tunnel.BuildEnv(os.Environ(), creds, region)
		return consoleReadyMsg{name: named.Display, cmd: cmd}
	}
}

func (a *App) reopenCmd() tea.Cmd {
	if a.client == nil {
		return nil
	}
	id := a.selectedConnID()
	if id == "" {
		return nil
	}
	client := a.client
	ctx := a.ctx
	return func() tea.Msg {
		var conn model.Connection
		err := client.Call(ctx, "reopen_rdp", daemon.ReopenParams{ID: id}, &conn)
		return reopenResultMsg{conn: conn, err: err}
	}
}

func (a *App) closeCmd() tea.Cmd {
	if a.client == nil {
		return nil
	}
	id := a.selectedConnID()
	if id == "" {
		return nil
	}
	client := a.client
	ctx := a.ctx
	return func() tea.Msg {
		err := client.Call(ctx, "close_connection", daemon.CloseParams{ID: id}, nil)
		return closeResultMsg{err: err}
	}
}

func (a *App) startInstanceCmd(id, name string) tea.Cmd {
	return a.instanceActionCmd(actionStart, id, name)
}

func (a *App) stopInstanceCmd(id string) tea.Cmd {
	return a.instanceActionCmd(actionStop, id, a.instanceDisplayName(id))
}

func (a *App) instanceActionCmd(action, id, name string) tea.Cmd {
	if a.session == nil {
		return nil
	}
	session := a.session
	ctx := a.ctx
	return func() tea.Msg {
		client := ec2.NewClient(session.Config)
		var err error
		switch action {
		case actionStart:
			err = ec2.Start(ctx, client, id)
		case actionStop:
			err = ec2.Stop(ctx, client, id)
		default:
			err = fmt.Errorf("unknown instance action %q", action)
		}
		return instanceActionResultMsg{action: action, id: id, name: name, err: err}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampSelection()
		return a, nil

	case loadedMsg:
		a.loading = false
		a.cfg = msg.cfg
		a.configPath = msg.cfg.Path
		a.session = msg.session
		a.accountID = msg.session.AccountID
		a.region = msg.session.Region
		a.profile = msg.session.Profile
		a.theme = msg.cfg.EffectiveColors(msg.session.AccountID)
		a.rdpOpts = msg.cfg.AccountOrDefault(msg.session.AccountID).RDP
		a.opener = msg.cfg.File.App.RDPOpener
		a.editor = msg.cfg.File.App.Editor
		a.quitAfter = msg.cfg.File.App.QuitAfterRDP
		a.states = append([]string(nil), msg.cfg.File.AWS.States...)
		a.instances = msg.instances
		a.rebuildNames("")
		if msg.notice != "" {
			a.setStatus(msg.notice, false)
		} else if len(msg.warnings) > 0 {
			a.setStatus(strings.Join(msg.warnings, " · "), true)
		} else {
			a.clearStatus()
		}
		return a, nil

	case startupErrMsg:
		a.loading = false
		a.fatal = msg.err.Error()
		a.fatalHint = msg.hint
		a.view = viewError
		a.setStatus("Startup failed", true)
		return a, nil

	case instancesMsg:
		selectedID := a.selectedInstanceID()
		a.instances = msg.instances
		a.rebuildNames(selectedID)
		if msg.note != "" {
			a.setStatus(msg.note, false)
		} else {
			a.setStatus(fmt.Sprintf("Loaded %d instance(s)", len(a.instances)), false)
		}
		return a, nil

	case instancesErrMsg:
		a.setStatus("Refresh failed: "+msg.err.Error(), true)
		return a, nil

	case instanceActionResultMsg:
		a.actionPending = false
		if msg.err != nil {
			a.setStatus(fmt.Sprintf("Could not %s %s: %v", msg.action, msg.name, msg.err), true)
			return a, nil
		}
		verb := "Start"
		if msg.action == actionStop {
			verb = "Stop"
		}
		note := fmt.Sprintf("%s requested for %s; refreshing…", verb, msg.name)
		a.setStatus(note, false)
		return a, a.refreshInstancesCmd(note)

	case daemonMsg:
		if msg.err != nil {
			a.setStatus("Daemon unavailable: "+msg.err.Error(), true)
			if !a.reconnect {
				a.reconnect = true
				return a, tea.Tick(time.Second, func(t time.Time) tea.Msg { return reconnectMsg{} })
			}
			return a, nil
		}
		if a.client != nil && a.client != msg.client {
			_ = a.client.Close()
		}
		a.client = msg.client
		a.info = msg.info
		a.listening = true
		a.reconnect = false
		a.mergeConns(msg.conns)
		return a, waitEvent(msg.client)

	case eventMsg:
		a.handleEvent(msg.ev)
		if a.quit {
			return a, tea.Quit
		}
		if a.listening && a.client != nil {
			return a, waitEvent(a.client)
		}
		return a, nil

	case eventsClosedMsg:
		a.listening = false
		a.client = nil
		a.setStatus("Lost connection to the daemon; reconnecting…", true)
		if !a.reconnect {
			a.reconnect = true
			return a, tea.Tick(time.Second, func(t time.Time) tea.Msg { return reconnectMsg{} })
		}
		return a, nil

	case reconnectMsg:
		if a.listening {
			a.reconnect = false
			return a, nil
		}
		a.reconnect = false
		return a, a.startDaemonCmd()

	case refreshConnsMsg:
		if msg.err != nil {
			a.setStatus("List connections failed: "+msg.err.Error(), true)
			return a, nil
		}
		a.mergeConns(msg.conns)
		return a, nil

	case connectResultMsg:
		if msg.err != nil {
			a.pendingReady = false
			a.setStatus("Connect failed: "+msg.err.Error(), true)
			return a, nil
		}
		if _, closed := a.closedIDs[msg.conn.ID]; closed {
			a.pendingReady = false
			a.setStatus("Connection exited before it could be displayed; see the daemon log.", true)
			return a, nil
		}
		a.conns[msg.conn.ID] = msg.conn
		a.rebuildConnOrder(msg.conn.ID)
		a.pendingReady = true
		a.setStatus(fmt.Sprintf("Starting tunnel to %s…", a.connDisplay(msg.conn)), false)
		return a, nil

	case reopenResultMsg:
		if msg.err != nil {
			a.setStatus("Reopen failed: "+msg.err.Error(), true)
			return a, nil
		}
		a.conns[msg.conn.ID] = msg.conn
		a.setStatus("Opened RDP for "+a.connDisplay(msg.conn), false)
		return a, nil

	case closeResultMsg:
		if msg.err != nil {
			a.setStatus("Close failed: "+msg.err.Error(), true)
		}
		return a, nil

	case consoleReadyMsg:
		a.consolePending = false
		if msg.err != nil {
			a.setStatus("Console session failed: "+msg.err.Error(), true)
			return a, nil
		}
		a.setStatus(fmt.Sprintf("Opening console session to %s…", msg.name), false)
		name := msg.name
		return a, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			return consoleDoneMsg{name: name, err: err}
		})

	case consoleDoneMsg:
		a.consolePending = false
		if msg.err != nil {
			a.setStatus(fmt.Sprintf("Console session to %s failed: %v (see the terminal output above)", msg.name, msg.err), true)
			return a, nil
		}
		a.setStatus(fmt.Sprintf("Console session ended for %s", msg.name), false)
		return a, nil

	case editorReadyMsg:
		a.setStatus("Editing configuration…", false)
		return a, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			return editorDoneMsg{err: err}
		})

	case editorDoneMsg:
		return a, a.reloadConfigAfterEdit(msg.err)

	case tickMsg:
		a.spinner++
		for id, at := range a.closedIDs {
			if time.Since(at) > 10*time.Minute {
				delete(a.closedIDs, id)
			}
		}
		if a.status != "" && time.Since(a.statusAt) > 8*time.Second && !a.loading && !a.actionPending && a.confirmStopID == "" && a.confirmID == "" {
			a.clearStatus()
		}
		return a, tickCmd()

	case tea.KeyMsg:
		return a.handleKey(msg)

	case tea.MouseMsg:
		return a.handleMouse(msg)
	}
	return a, nil
}

func (a *App) handleEvent(ev ipc.Event) {
	switch ev.Event {
	case "connection.updated":
		if ev.Connection == nil {
			return
		}
		c := *ev.Connection
		a.conns[c.ID] = c
		a.rebuildConnOrder(c.ID)
		displayName := a.connDisplay(c)
		switch c.State {
		case model.StateReady:
			if c.RDPError != "" {
				a.pendingReady = false
				a.setStatus(fmt.Sprintf("RDP file ready for %s but opening failed: %s (file: %s) — the tunnel stays active", displayName, c.RDPError, c.RDPPath), true)
			} else if c.RDPOpened {
				wasPending := a.pendingReady
				a.pendingReady = false
				if wasPending && a.quitAfter {
					a.summary = fmt.Sprintf("RDP started for %s via 127.0.0.1:%d (tunnel %s)", displayName, c.LocalPort, c.ID)
					a.quit = true
					return
				}
				a.setStatus(fmt.Sprintf("RDP started for %s on 127.0.0.1:%d", displayName, c.LocalPort), false)
			} else {
				a.setStatus(fmt.Sprintf("Tunnel ready for %s on 127.0.0.1:%d", displayName, c.LocalPort), false)
			}
		}
	case "connection.closed":
		reason := ev.Reason
		message := strings.TrimSpace(ev.Message)
		var id string
		if ev.Connection != nil {
			id = ev.Connection.ID
			delete(a.conns, id)
		} else {
			id = a.selectedConnID()
		}
		if a.confirmID == id {
			a.confirmID = ""
		}
		if id != "" {
			a.closedIDs[id] = time.Now()
		}
		a.pendingReady = false
		a.rebuildConnOrder("")
		if reason != "" {
			if message != "" {
				reason += ": " + lastLines(message, 3)
			}
			a.setStatus("Connection closed: "+reason, true)
		}
	case "daemon.shutting_down":
		a.setStatus("Daemon is shutting down", true)
	}
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return a, tea.Quit
	}
	if a.filterEditing {
		return a.handleFilterKey(msg)
	}
	if key == "/" {
		if (a.view == viewInstances || a.view == viewConnections) && a.confirmID == "" && a.confirmStopID == "" {
			a.startFilter()
		}
		return a, nil
	}
	if key == "e" {
		if a.loading {
			a.setStatus("Wait for startup to finish before editing the config", true)
			return a, nil
		}
		return a, a.editConfigCmd()
	}
	if a.view == viewHelp {
		switch key {
		case "esc", "?", "q", "h":
			a.view = a.prevView
		}
		return a, nil
	}
	if a.view == viewError {
		switch key {
		case "q", "esc":
			return a, tea.Quit
		case "R", "r":
			a.loading = true
			a.fatal = ""
			a.view = viewInstances
			a.setStatus("Retrying…", false)
			return a, a.loadCmd()
		}
		return a, nil
	}
	if a.view == viewInstanceInfo {
		total := len(a.instanceInfoLines(a.contentWidth()))
		switch key {
		case "q":
			return a, tea.Quit
		case "?":
			a.prevView = a.view
			a.view = viewHelp
		case "esc", "i":
			a.view = viewInstances
			a.infoOffset = 0
		case "up", "k":
			a.infoOffset--
			a.clampInfoOffset(total)
		case "down", "j":
			a.infoOffset++
			a.clampInfoOffset(total)
		case "pgup":
			a.infoOffset -= a.infoPageSize()
			a.clampInfoOffset(total)
		case "pgdown":
			a.infoOffset += a.infoPageSize()
			a.clampInfoOffset(total)
		case "home", "g":
			a.infoOffset = 0
		case "end", "G":
			a.infoOffset = total
			a.clampInfoOffset(total)
		}
		return a, nil
	}
	if a.confirmID != "" {
		switch key {
		case "x":
			a.confirmID = ""
			return a, a.closeCmd()
		case "esc", "n":
			a.confirmID = ""
			a.setStatus("Close cancelled", false)
		}
		return a, nil
	}
	if a.confirmStopID != "" {
		switch key {
		case "S":
			id := a.confirmStopID
			a.confirmStopID = ""
			inst := findInstance(a.instances, id)
			name := a.instanceDisplayName(id)
			if reason := instanceActionBlockedMessage(name, inst.State, actionStop); reason != "" {
				a.setStatus(reason, true)
				return a, nil
			}
			if a.session == nil {
				a.setStatus("AWS session unavailable; cannot stop instances", true)
				return a, nil
			}
			a.actionPending = true
			a.setStatus("Stopping "+name+"…", false)
			return a, a.stopInstanceCmd(id)
		case "esc", "n":
			a.confirmStopID = ""
			a.setStatus("Stop cancelled", false)
		}
		return a, nil
	}
	switch key {
	case "q":
		return a, tea.Quit
	case "?":
		a.prevView = a.view
		a.view = viewHelp
		return a, nil
	case "tab":
		if a.view == viewInstances {
			a.view = viewConnections
			a.clampSelection()
			return a, a.refreshConnsCmd()
		}
		a.view = viewInstances
		a.clampSelection()
		return a, nil
	case "esc":
		a.confirmID = ""
		a.confirmStopID = ""
		if a.currentFilter() != "" {
			a.clearCurrentFilter()
			return a, nil
		}
		a.clearStatus()
		return a, nil
	}

	if a.loading || a.fatal != "" {
		return a, nil
	}
	if a.view == viewInstances {
		var selInst ec2.Instance
		var selName string
		if len(a.visibleNamed) > 0 && a.selected >= 0 && a.selected < len(a.visibleNamed) {
			selName = a.visibleNamed[a.selected].Display
			selInst = findInstance(a.instances, a.visibleNamed[a.selected].InstanceID)
		}
		switch key {
		case "up", "k":
			a.selected--
			a.clampSelection()
		case "down", "j":
			a.selected++
			a.clampSelection()
		case "pgup":
			a.selected -= a.pageSize()
			a.clampSelection()
		case "pgdown":
			a.selected += a.pageSize()
			a.clampSelection()
		case "home", "g":
			a.selected = 0
			a.clampSelection()
		case "end", "G":
			a.selected = len(a.visibleNamed) - 1
			a.clampSelection()
		case "i":
			if len(a.visibleNamed) == 0 {
				return a, nil
			}
			a.infoOffset = 0
			a.view = viewInstanceInfo
		case "s":
			if len(a.visibleNamed) == 0 {
				return a, nil
			}
			if a.actionPending {
				a.setStatus("An instance action is already in progress", true)
				return a, nil
			}
			if a.session == nil {
				a.setStatus("AWS session unavailable; cannot start instances", true)
				return a, nil
			}
			if reason := instanceActionBlockedMessage(selName, selInst.State, actionStart); reason != "" {
				a.setStatus(reason, true)
				return a, nil
			}
			a.actionPending = true
			a.setStatus("Starting "+selName+"…", false)
			return a, a.startInstanceCmd(selInst.InstanceID, selName)
		case "S":
			if len(a.visibleNamed) == 0 {
				return a, nil
			}
			if a.actionPending {
				a.setStatus("An instance action is already in progress", true)
				return a, nil
			}
			if a.session == nil {
				a.setStatus("AWS session unavailable; cannot stop instances", true)
				return a, nil
			}
			if reason := instanceActionBlockedMessage(selName, selInst.State, actionStop); reason != "" {
				a.setStatus(reason, true)
				return a, nil
			}
			a.confirmStopID = selInst.InstanceID
			prompt := fmt.Sprintf("Stop %s? Press S again, esc to cancel", selName)
			if n := a.tunnelCount(selInst.InstanceID); n > 0 {
				prompt = fmt.Sprintf("Stop %s? %d active tunnel(s) will close. Press S again, esc to cancel", selName, n)
			}
			a.setStatus(prompt, true)
		case "r":
			if len(a.visibleNamed) == 0 {
				return a, nil
			}
			if !selInst.IsWindows() {
				a.setStatus("RDP is only supported for Windows instances. Press c for a console session.", true)
				return a, nil
			}
			if reason := instanceRunningMessage(selName, selInst.State); reason != "" {
				a.setStatus(reason, true)
				return a, nil
			}
			if a.client == nil || a.session == nil {
				a.setStatus("Daemon unavailable; reconnecting…", true)
				if !a.reconnect {
					a.reconnect = true
					return a, tea.Tick(time.Second, func(t time.Time) tea.Msg { return reconnectMsg{} })
				}
				return a, nil
			}
			a.setStatus("Requesting AWS credentials…", false)
			a.pendingReady = true
			return a, a.connectCmd()
		case "c":
			if len(a.visibleNamed) == 0 || a.consolePending {
				return a, nil
			}
			if a.session == nil {
				a.setStatus("AWS session unavailable; cannot open a console session", true)
				return a, nil
			}
			if reason := instanceRunningMessage(selName, selInst.State); reason != "" {
				a.setStatus(reason, true)
				return a, nil
			}
			a.consolePending = true
			a.setStatus("Resolving AWS credentials…", false)
			return a, a.prepareConsoleCmd()
		case "R":
			a.setStatus("Refreshing instances…", false)
			return a, a.refreshInstancesCmd("")
		}
		return a, nil
	}

	// Connections view.
	switch key {
	case "up", "k":
		a.connSel--
		a.clampSelection()
	case "down", "j":
		a.connSel++
		a.clampSelection()
	case "pgup":
		a.connSel -= a.pageSize()
		a.clampSelection()
	case "pgdown":
		a.connSel += a.pageSize()
		a.clampSelection()
	case "home", "g":
		a.connSel = 0
		a.clampSelection()
	case "end", "G":
		a.connSel = len(a.visibleConns) - 1
		a.clampSelection()
	case "o", "enter":
		if id := a.selectedConnID(); id != "" {
			a.setStatus("Opening RDP file…", false)
			return a, a.reopenCmd()
		}
	case "x":
		if id := a.selectedConnID(); id != "" {
			a.confirmID = id
			a.setStatus(fmt.Sprintf("Press x again to close %s, esc to cancel", a.connDisplay(a.conns[id])), true)
		}
	}
	return a, nil
}

func (a *App) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return a, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		switch a.view {
		case viewInstances:
			a.selected -= 3
			a.clampSelection()
		case viewInstanceInfo:
			a.infoOffset -= 3
			a.clampInfoOffset(len(a.instanceInfoLines(a.contentWidth())))
		default:
			a.connSel -= 3
			a.clampSelection()
		}
		return a, nil
	case tea.MouseButtonWheelDown:
		switch a.view {
		case viewInstances:
			a.selected += 3
			a.clampSelection()
		case viewInstanceInfo:
			a.infoOffset += 3
			a.clampInfoOffset(len(a.instanceInfoLines(a.contentWidth())))
		default:
			a.connSel += 3
			a.clampSelection()
		}
		return a, nil
	case tea.MouseButtonLeft:
		if a.view == viewInstanceInfo {
			return a, nil
		}
		row := msg.Y - 1
		if row < 0 {
			return a, nil
		}
		if a.view == viewInstances {
			idx := a.offset + row
			if idx >= 0 && idx < len(a.visibleNamed) {
				a.selected = idx
			}
		} else {
			idx := a.connOffset + row
			if idx >= 0 && idx < len(a.visibleConns) {
				a.connSel = idx
			}
		}
	}
	return a, nil
}

func (a *App) rebuildNames(keepID string) {
	instances := make([]display.Instance, len(a.instances))
	for i, inst := range a.instances {
		instances[i] = display.Instance{
			InstanceID: inst.InstanceID,
			Name:       inst.Name,
			LaunchTime: inst.LaunchTime,
		}
	}
	a.named = display.Names(instances)
	a.nameByID = make(map[string]display.Named, len(a.named))
	for _, n := range a.named {
		a.nameByID[n.InstanceID] = n
	}
	a.refreshVisible(keepID, a.selectedConnID())
}

func (a *App) mergeConns(in []model.Connection) {
	a.conns = make(map[string]model.Connection, len(in))
	for _, c := range in {
		a.conns[c.ID] = c
		delete(a.closedIDs, c.ID)
	}
	a.rebuildConnOrder("")
}

func (a *App) rebuildConnOrder(keepID string) {
	if keepID == "" {
		keepID = a.selectedConnID()
	}
	order := make([]string, 0, len(a.conns))
	for id := range a.conns {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		ci, cj := a.conns[order[i]], a.conns[order[j]]
		if !ci.StartedAt.Equal(cj.StartedAt) {
			return ci.StartedAt.Before(cj.StartedAt)
		}
		return order[i] < order[j]
	})
	a.connOrder = order
	a.refreshVisible(a.selectedInstanceID(), keepID)
}

// refreshVisible rebuilds the filtered row lists and restores selection by ID
// when possible.
func (a *App) refreshVisible(keepInstanceID, keepConnID string) {
	instanceQuery := strings.ToLower(a.filterQueries[0])
	a.visibleNamed = make([]display.Named, 0, len(a.named))
	for _, n := range a.named {
		if instanceQuery == "" || strings.Contains(a.instanceSearchText(n), instanceQuery) {
			a.visibleNamed = append(a.visibleNamed, n)
		}
	}
	connQuery := strings.ToLower(a.filterQueries[1])
	a.visibleConns = make([]string, 0, len(a.connOrder))
	for _, id := range a.connOrder {
		if connQuery == "" || strings.Contains(a.connSearchText(id), connQuery) {
			a.visibleConns = append(a.visibleConns, id)
		}
	}

	a.selected = 0
	if keepInstanceID != "" {
		for i, n := range a.visibleNamed {
			if n.InstanceID == keepInstanceID {
				a.selected = i
				break
			}
		}
	}
	a.connSel = 0
	if keepConnID != "" {
		for i, id := range a.visibleConns {
			if id == keepConnID {
				a.connSel = i
				break
			}
		}
	}
	a.clampSelection()
}

func (a *App) instanceSearchText(n display.Named) string {
	inst := findInstance(a.instances, n.InstanceID)
	return strings.ToLower(strings.Join([]string{
		n.Display,
		inst.State,
		inst.InstanceID,
		inst.Type,
		inst.PrivateIP,
		inst.Platform,
		strconv.Itoa(a.tunnelCount(n.InstanceID)),
	}, "\x00"))
}

func (a *App) connSearchText(id string) string {
	c := a.conns[id]
	return strings.ToLower(strings.Join([]string{
		a.connDisplay(c),
		c.AccountID,
		strconv.Itoa(c.LocalPort),
		stateLabel(c),
		age(c.StartedAt),
		c.ID,
	}, "\x00"))
}

func (a *App) clampSelection() {
	if len(a.visibleNamed) == 0 {
		a.selected = 0
		a.offset = 0
	} else {
		if a.selected < 0 {
			a.selected = 0
		}
		if a.selected >= len(a.visibleNamed) {
			a.selected = len(a.visibleNamed) - 1
		}
		page := a.pageSize()
		if a.selected < a.offset {
			a.offset = a.selected
		}
		if a.selected >= a.offset+page {
			a.offset = a.selected - page + 1
		}
		maxOffset := len(a.visibleNamed) - page
		if maxOffset < 0 {
			maxOffset = 0
		}
		if a.offset > maxOffset {
			a.offset = maxOffset
		}
		if a.offset < 0 {
			a.offset = 0
		}
	}
	if len(a.visibleConns) == 0 {
		a.connSel = 0
		a.connOffset = 0
	} else {
		if a.connSel < 0 {
			a.connSel = 0
		}
		if a.connSel >= len(a.visibleConns) {
			a.connSel = len(a.visibleConns) - 1
		}
		page := a.pageSize()
		if a.connSel < a.connOffset {
			a.connOffset = a.connSel
		}
		if a.connSel >= a.connOffset+page {
			a.connOffset = a.connSel - page + 1
		}
		maxOffset := len(a.visibleConns) - page
		if maxOffset < 0 {
			maxOffset = 0
		}
		if a.connOffset > maxOffset {
			a.connOffset = maxOffset
		}
		if a.connOffset < 0 {
			a.connOffset = 0
		}
	}
}

func (a *App) pageSize() int {
	h := a.height - 4
	if h < 1 {
		h = 1
	}
	return h
}

func (a *App) infoPageSize() int {
	h := a.height - 3
	if h < 1 {
		h = 1
	}
	return h
}

func (a *App) clampInfoOffset(total int) {
	max := total - a.infoPageSize()
	if max < 0 {
		max = 0
	}
	if a.infoOffset < 0 {
		a.infoOffset = 0
	}
	if a.infoOffset > max {
		a.infoOffset = max
	}
}

func (a *App) selectedConnID() string {
	if a.connSel >= 0 && a.connSel < len(a.visibleConns) {
		return a.visibleConns[a.connSel]
	}
	return ""
}

func (a *App) selectedInstanceID() string {
	if a.selected >= 0 && a.selected < len(a.visibleNamed) {
		return a.visibleNamed[a.selected].InstanceID
	}
	return ""
}

func (a *App) filterSlot(v viewMode) int {
	if v == viewConnections {
		return 1
	}
	return 0
}

func (a *App) currentFilter() string {
	if a.view != viewInstances && a.view != viewConnections {
		return ""
	}
	return a.filterQueries[a.filterSlot(a.view)]
}

func (a *App) startFilter() {
	a.filterView = a.view
	a.filterPrev = a.filterQueries[a.filterSlot(a.view)]
	a.filterInput = []rune(a.filterPrev)
	a.filterEditing = true
	a.clearStatus()
}

func (a *App) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		keepInstanceID := a.selectedInstanceID()
		keepConnID := a.selectedConnID()
		a.filterEditing = false
		a.filterInput = nil
		a.filterQueries[a.filterSlot(a.filterView)] = a.filterPrev
		a.refreshVisible(keepInstanceID, keepConnID)
	case tea.KeyEnter:
		a.filterEditing = false
		a.filterInput = nil
		a.clearStatus()
	case tea.KeyBackspace, tea.KeyCtrlH, tea.KeyDelete:
		if len(a.filterInput) > 0 {
			a.filterInput = a.filterInput[:len(a.filterInput)-1]
			a.applyFilterInput()
		}
	case tea.KeyCtrlU:
		if len(a.filterInput) > 0 {
			a.filterInput = nil
			a.applyFilterInput()
		}
	case tea.KeySpace:
		a.filterInput = append(a.filterInput, ' ')
		a.applyFilterInput()
	case tea.KeyRunes:
		if len(msg.Runes) > 0 {
			a.filterInput = append(a.filterInput, msg.Runes...)
			a.applyFilterInput()
		}
	}
	return a, nil
}

func (a *App) applyFilterInput() {
	keepInstanceID := a.selectedInstanceID()
	keepConnID := a.selectedConnID()
	a.filterQueries[a.filterSlot(a.filterView)] = string(a.filterInput)
	a.refreshVisible(keepInstanceID, keepConnID)
}

func (a *App) clearCurrentFilter() {
	keepInstanceID := a.selectedInstanceID()
	keepConnID := a.selectedConnID()
	a.filterQueries[a.filterSlot(a.view)] = ""
	a.filterInput = nil
	a.filterEditing = false
	a.refreshVisible(keepInstanceID, keepConnID)
}

func (a *App) filterStats(v viewMode) (matched, total int) {
	switch v {
	case viewConnections:
		return len(a.visibleConns), len(a.connOrder)
	case viewInstances:
		return len(a.visibleNamed), len(a.named)
	default:
		return 0, 0
	}
}

func (a *App) connDisplay(c model.Connection) string {
	if n, ok := a.nameByID[c.InstanceID]; ok {
		return n.Display
	}
	if strings.TrimSpace(c.InstanceName) != "" {
		return display.Sanitize(c.InstanceName)
	}
	return c.InstanceID
}

func (a *App) tunnelCount(instanceID string) int {
	n := 0
	for _, c := range a.conns {
		if c.InstanceID == instanceID && (c.State == model.StateStarting || c.State == model.StateReady || c.State == model.StateClosing) {
			n++
		}
	}
	return n
}

func (a *App) setStatus(s string, isErr bool) {
	a.status = s
	a.statusErr = isErr
	a.statusAt = time.Now()
}

func (a *App) clearStatus() {
	a.status = ""
	a.statusErr = false
}

func findInstance(in []ec2.Instance, id string) ec2.Instance {
	for _, inst := range in {
		if inst.InstanceID == id {
			return inst
		}
	}
	return ec2.Instance{InstanceID: id}
}

func (a *App) instanceDisplayName(id string) string {
	if n, ok := a.nameByID[id]; ok {
		return n.Display
	}
	return id
}

// instanceActionBlockedMessage returns a user-facing reason when an instance
// cannot be started or stopped in its current state.
func instanceActionBlockedMessage(name, state, action string) string {
	switch state {
	case "running":
		if action == actionStart {
			return name + " is already running."
		}
	case "stopped":
		if action == actionStop {
			return name + " is already stopped."
		}
	case "pending":
		return name + " is still starting; wait for it to finish."
	case "stopping":
		return name + " is still stopping; wait for it to finish."
	case "shutting-down":
		return name + " is shutting down."
	case "terminated":
		return name + " is terminated."
	default:
		return fmt.Sprintf("%s has an unknown state (%q); cannot %s it.", name, state, action)
	}
	return ""
}

// instanceRunningMessage returns a reason when console/RDP is requested for an
// instance that is not running.
func instanceRunningMessage(name, state string) string {
	switch state {
	case "running":
		return ""
	case "stopped":
		return name + " is stopped; press s to start it."
	case "pending":
		return name + " is still starting; try again when it is running."
	case "stopping":
		return name + " is stopping; wait for it to stop, then press s to start it."
	case "shutting-down":
		return name + " is shutting down and cannot be used."
	case "terminated":
		return name + " is terminated and cannot be used."
	default:
		return fmt.Sprintf("%s is in state %q; console and RDP require a running instance.", name, state)
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " ")
}
