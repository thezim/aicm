package daemon

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go-aws/internal/ipc"
	"go-aws/internal/model"
	"go-aws/internal/platform"
	"go-aws/internal/rdp"
	"go-aws/internal/tunnel"
)

// Options configures a daemon instance.
type Options struct {
	ConnectTimeout time.Duration
	IdleExit       time.Duration
	Version        string
}

func (o *Options) normalize() {
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 30 * time.Second
	}
	if o.IdleExit <= 0 {
		o.IdleExit = 5 * time.Second
	}
	if o.Version == "" {
		o.Version = "dev"
	}
}

// ConnectParams is sent by the TUI. Credentials are used only for the child
// AWS CLI environment and are never stored.
type ConnectParams struct {
	AccountID    string            `json:"account_id"`
	InstanceID   string            `json:"instance_id"`
	InstanceName string            `json:"instance_name,omitempty"`
	Platform     string            `json:"platform,omitempty"`
	Region       string            `json:"region"`
	RDP          model.RDPOptions  `json:"rdp"`
	Opener       []string          `json:"opener,omitempty"`
	Credentials  model.Credentials `json:"credentials"`
}

type ReopenParams struct {
	ID string `json:"id"`
}

type CloseParams struct {
	ID  string `json:"id,omitempty"`
	All bool   `json:"all,omitempty"`
}

type connection struct {
	rec      model.Connection
	opts     model.RDPOptions
	opener   []string
	cmd      *exec.Cmd
	tree     *platform.Tree
	cancel   context.CancelFunc
	tail     *tunnel.Tail
	closeWhy string
	started  time.Time
}

// Manager owns all live tunnels.
type Manager struct {
	mu             sync.Mutex
	conns          map[string]*connection
	connectTimeout time.Duration
	awsBin         string
	emit           func(ipc.Event)
	wg             sync.WaitGroup
	shuttingDown   bool
	statePath      string
}

func NewManager(opts Options) *Manager {
	opts.normalize()
	statePath, _ := platform.StatePath()
	return &Manager{
		conns:          make(map[string]*connection),
		connectTimeout: opts.ConnectTimeout,
		awsBin:         tunnel.AWSBinary(),
		statePath:      statePath,
	}
}

func (m *Manager) SetEmitter(fn func(ipc.Event)) {
	m.mu.Lock()
	m.emit = fn
	m.mu.Unlock()
}

func (m *Manager) emitEvent(ev ipc.Event) {
	m.mu.Lock()
	fn := m.emit
	m.mu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.conns)
}

func (m *Manager) List() []model.Connection {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Connection, 0, len(m.conns))
	for _, c := range m.conns {
		out = append(out, c.rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

func (m *Manager) Get(id string) (model.Connection, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok {
		return model.Connection{}, false
	}
	return c.rec, true
}

// isWindowsPlatform reports whether EC2's platform string denotes Windows.
func isWindowsPlatform(platform string) bool {
	return strings.Contains(strings.ToLower(platform), "windows")
}

// Connect starts a tunnel asynchronously and returns its initial state.
func (m *Manager) Connect(params ConnectParams) (model.Connection, error) {
	if strings.TrimSpace(params.InstanceID) == "" {
		return model.Connection{}, &ipc.RPCError{Code: "invalid", Message: "instance_id is required"}
	}
	if strings.TrimSpace(params.Region) == "" {
		return model.Connection{}, &ipc.RPCError{Code: "invalid", Message: "region is required"}
	}
	if !isWindowsPlatform(params.Platform) {
		return model.Connection{}, &ipc.RPCError{Code: "unsupported", Message: "RDP is only supported for Windows instances"}
	}
	if params.RDP.RemotePort <= 0 || params.RDP.RemotePort > 65535 {
		return model.Connection{}, &ipc.RPCError{Code: "invalid", Message: fmt.Sprintf("invalid remote port %d", params.RDP.RemotePort)}
	}
	if params.Credentials.AccessKeyID == "" || params.Credentials.SecretAccessKey == "" {
		return model.Connection{}, &ipc.RPCError{Code: "invalid", Message: "AWS credentials are required"}
	}
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return model.Connection{}, &ipc.RPCError{Code: "shutting_down", Message: "daemon is shutting down"}
	}
	m.mu.Unlock()

	localPort, err := tunnel.AllocatePort()
	if err != nil {
		return model.Connection{}, &ipc.RPCError{Code: "port", Message: fmt.Sprintf("allocate local port: %v", err)}
	}
	args, err := tunnel.Command(m.awsBin, params.InstanceID, params.RDP.RemotePort, localPort, params.Region)
	if err != nil {
		return model.Connection{}, &ipc.RPCError{Code: "invalid", Message: err.Error()}
	}
	id, err := newConnectionID()
	if err != nil {
		return model.Connection{}, fmt.Errorf("generate connection id: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	tail := tunnel.NewTail(64 * 1024)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = tunnel.BuildEnv(os.Environ(), params.Credentials, params.Region)
	cmd.Stdout = tail
	cmd.Stderr = tail
	cmd.Stdin = nil
	tree := platform.NewTree()
	tree.Configure(cmd)

	rec := model.Connection{
		ID:           id,
		AccountID:    params.AccountID,
		InstanceID:   params.InstanceID,
		InstanceName: params.InstanceName,
		Region:       params.Region,
		LocalPort:    localPort,
		State:        model.StateStarting,
		StartedAt:    time.Now().UTC(),
	}
	c := &connection{
		rec:     rec,
		opts:    params.RDP,
		opener:  params.Opener,
		cmd:     cmd,
		tree:    tree,
		cancel:  cancel,
		tail:    tail,
		started: time.Now(),
	}

	if err := cmd.Start(); err != nil {
		cancel()
		tree.Close()
		return model.Connection{}, &ipc.RPCError{Code: "start", Message: fmt.Sprintf("start aws CLI: %v", err)}
	}
	// Credentials live only in the child process environment from here on.
	cmd.Env = nil
	if err := tree.Attach(cmd); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		cancel()
		tree.Close()
		return model.Connection{}, &ipc.RPCError{Code: "process", Message: fmt.Sprintf("supervise aws CLI: %v", err)}
	}

	m.mu.Lock()
	m.conns[id] = c
	m.persistLocked()
	m.mu.Unlock()

	log.Printf("tunnel %s starting: instance=%s local_port=%d region=%s account=%s",
		id, params.InstanceID, localPort, params.Region, params.AccountID)
	m.wg.Add(1)
	go m.lifecycle(ctx, c)
	m.emitEvent(ipc.Event{Event: "connection.updated", Connection: ptr(rec)})
	return rec, nil
}

// lifecycle owns readiness detection, cleanup, and the process wait.
func (m *Manager) lifecycle(ctx context.Context, c *connection) {
	defer m.wg.Done()
	waitCh := make(chan error, 1)
	go func() { waitCh <- c.cmd.Wait() }()

	ready := false
	startup := time.NewTimer(m.connectTimeout)
	defer startup.Stop()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	var timeoutC <-chan time.Time = startup.C

	for {
		select {
		case err := <-waitCh:
			if ready {
				m.finish(c, "AWS CLI exited", tailExcerpt(c.tail))
			} else {
				reason := "AWS CLI exited before the tunnel became ready"
				if err != nil {
					reason = fmt.Sprintf("AWS CLI exited before ready: %v", err)
				}
				m.finish(c, reason, tailExcerpt(c.tail))
			}
			return
		case <-ctx.Done():
			c.tree.Kill()
			select {
			case <-waitCh:
			case <-time.After(3 * time.Second):
			}
			m.finish(c, m.takeCloseReason(c), "")
			return
		case <-timeoutC:
			c.tree.Kill()
			select {
			case <-waitCh:
			case <-time.After(3 * time.Second):
			}
			m.finish(c, fmt.Sprintf("SSM session did not become ready within %s", m.connectTimeout), tailExcerpt(c.tail))
			return
		case <-ticker.C:
			if ready {
				continue
			}
			if tunnel.Ready(c.tail.String()) || tunnel.PortOpen(c.rec.LocalPort) {
				ready = true
				startup.Stop()
				timeoutC = nil
				m.markReady(c)
			}
		}
	}
}

func (m *Manager) markReady(c *connection) {
	tail := tailExcerpt(c.tail)
	content, err := rdp.Build(c.opts, c.rec.LocalPort)
	var path string
	var rdpErr error
	if err != nil {
		rdpErr = err
	} else {
		path = filepath.Join(mustRDPDir(), rdp.FileName(c.rec.InstanceName, c.rec.InstanceID, c.rec.ID, c.rec.LocalPort))
		if err := rdp.Write(path, content); err != nil {
			rdpErr = fmt.Errorf("write RDP file: %w", err)
			path = ""
		}
	}

	opened := false
	if rdpErr == nil {
		openCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := rdp.Open(openCtx, c.opener, path); err != nil {
			rdpErr = fmt.Errorf("open RDP file: %w", err)
		} else {
			opened = true
		}
		cancel()
	}

	now := time.Now().UTC()
	m.mu.Lock()
	if cur, ok := m.conns[c.rec.ID]; ok && cur == c {
		c.rec.State = model.StateReady
		c.rec.ReadyAt = &now
		c.rec.RDPPath = path
		c.rec.RDPOpened = opened
		if rdpErr != nil {
			c.rec.RDPError = rdpErr.Error()
		} else {
			c.rec.RDPError = ""
		}
		c.rec.OutputTail = tail
		m.persistLocked()
	}
	rec := c.rec
	m.mu.Unlock()
	m.emitEvent(ipc.Event{Event: "connection.updated", Connection: ptr(rec)})
}

func (m *Manager) finish(c *connection, reason, message string) {
	m.mu.Lock()
	if cur, ok := m.conns[c.rec.ID]; ok && cur == c {
		c.rec.State = model.StateClosing
		c.rec.OutputTail = tailExcerpt(c.tail)
		if c.rec.Error == "" {
			c.rec.Error = reason
		}
	}
	m.mu.Unlock()

	// Kill the process tree before dropping the crash-recovery entry, so a
	// daemon crash during cleanup still leaves a record for reconciliation.
	c.tree.Kill()
	c.tree.Close()
	c.cancel()

	m.mu.Lock()
	if cur, ok := m.conns[c.rec.ID]; ok && cur == c {
		delete(m.conns, c.rec.ID)
	}
	m.persistLocked()
	rec := c.rec
	rec.OutputTail = tailExcerpt(c.tail)
	m.mu.Unlock()

	if rec.RDPPath != "" {
		_ = os.Remove(rec.RDPPath)
	}
	log.Printf("tunnel %s closed: %s", rec.ID, reason)
	if message != "" {
		log.Printf("tunnel %s output tail: %s", rec.ID, message)
	}
	m.emitEvent(ipc.Event{
		Event:      "connection.closed",
		Connection: ptr(rec),
		Reason:     reason,
		Message:    message,
	})
}

func (m *Manager) takeCloseReason(c *connection) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.closeWhy != "" {
		return c.closeWhy
	}
	return "connection closed"
}

// Close cancels one connection. The lifecycle goroutine performs cleanup and
// emits connection.closed.
func (m *Manager) Close(id, reason string) error {
	m.mu.Lock()
	c, ok := m.conns[id]
	if !ok {
		m.mu.Unlock()
		return &ipc.RPCError{Code: "not_found", Message: "connection not found: " + id}
	}
	if reason == "" {
		reason = "closed by user"
	}
	c.closeWhy = reason
	c.rec.State = model.StateClosing
	rec := c.rec
	m.mu.Unlock()
	m.emitEvent(ipc.Event{Event: "connection.updated", Connection: ptr(rec)})
	c.cancel()
	return nil
}

// CloseAll cancels every connection.
func (m *Manager) CloseAll(reason string) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.conns))
	for id, c := range m.conns {
		if reason != "" {
			c.closeWhy = reason
		}
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Close(id, reason)
	}
}

// Reopen launches the stored .rdp file again.
func (m *Manager) Reopen(id string) (model.Connection, error) {
	m.mu.Lock()
	c, ok := m.conns[id]
	if !ok {
		m.mu.Unlock()
		return model.Connection{}, &ipc.RPCError{Code: "not_found", Message: "connection not found: " + id}
	}
	path := c.rec.RDPPath
	opener := c.opener
	if path == "" {
		m.mu.Unlock()
		return model.Connection{}, &ipc.RPCError{Code: "no_rdp", Message: "connection has no RDP file yet"}
	}
	m.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		return model.Connection{}, &ipc.RPCError{Code: "no_rdp", Message: fmt.Sprintf("RDP file unavailable: %v", err)}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := rdp.Open(ctx, opener, path)
	cancel()
	now := time.Now().UTC()
	m.mu.Lock()
	if cur, ok := m.conns[id]; ok && cur == c {
		if err != nil {
			c.rec.RDPError = fmt.Sprintf("open RDP file: %v", err)
			c.rec.RDPOpened = false
		} else {
			c.rec.RDPError = ""
			c.rec.RDPOpened = true
			c.rec.ReadyAt = &now
		}
	}
	rec := c.rec
	m.mu.Unlock()
	m.emitEvent(ipc.Event{Event: "connection.updated", Connection: ptr(rec)})
	if err != nil {
		return rec, &ipc.RPCError{Code: "open", Message: fmt.Sprintf("open RDP file: %v", err)}
	}
	return rec, nil
}

// Shutdown closes all tunnels and waits for cleanup to complete.
func (m *Manager) Shutdown(timeout time.Duration) {
	m.mu.Lock()
	m.shuttingDown = true
	ids := make([]string, 0, len(m.conns))
	for id := range m.conns {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Close(id, "daemon shutting down")
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		m.clearState()
	case <-time.After(timeout):
		log.Printf("daemon: timed out waiting for tunnel cleanup; keeping crash state")
	}
}

func (m *Manager) isShuttingDown() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shuttingDown
}

func mustRDPDir() string {
	dir, err := platform.RDPDir()
	if err != nil {
		return os.TempDir()
	}
	return dir
}

func tailExcerpt(t *tunnel.Tail) string {
	s := strings.TrimSpace(t.String())
	if len(s) > 4096 {
		s = s[len(s)-4096:]
	}
	return s
}

func ptr(c model.Connection) *model.Connection { return &c }

func newConnectionID() (string, error) {
	b := make([]byte, 8)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// persistLocked writes crash-recovery state. The manager lock must be held.
func (m *Manager) persistLocked() {
	if m.statePath == "" {
		return
	}
	if len(m.conns) == 0 {
		_ = os.Remove(m.statePath)
		return
	}
	sf := stateFile{Version: 1}
	for _, c := range m.conns {
		entry := stateEntry{
			PID:        0,
			LocalPort:  c.rec.LocalPort,
			RDPPath:    c.rec.RDPPath,
			InstanceID: c.rec.InstanceID,
			AccountID:  c.rec.AccountID,
			Command:    strings.Join(c.cmd.Args, " "),
		}
		if c.cmd.Process != nil {
			entry.PID = c.cmd.Process.Pid
		}
		sf.Entries = append(sf.Entries, entry)
	}
	raw, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(m.statePath, raw)
}

func (m *Manager) clearState() {
	if m.statePath == "" {
		return
	}
	_ = os.Remove(m.statePath)
}
