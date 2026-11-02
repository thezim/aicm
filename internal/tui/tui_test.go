package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	aws "github.com/aws/aws-sdk-go-v2/aws"

	"go-aws/internal/awsconfig"
	"go-aws/internal/display"
	"go-aws/internal/ec2"
	"go-aws/internal/ipc"
	"go-aws/internal/model"
)

func testApp() *App {
	a := &App{
		width:  90,
		height: 12,
		view:   viewInstances,
		theme: model.Colors{
			ListForeground: "#cdd6f4", ListBackground: "#1e1e2e",
			SelectedForeground: "#1e1e2e", SelectedBackground: "#89b4fa",
			HeaderForeground: "#89b4fa", HeaderBackground: "#1e1e2e",
			StatusForeground: "#89b4fa", StatusBackground: "#1e1e2e",
			TransientForeground: "#89b4fa", TransientBackground: "#1e1e2e",
			TransientErrorForeground: "#f38ba8",
			KeysForeground:           "#89b4fa", KeysBackground: "#1e1e2e",
			HelpForeground: "#cdd6f4", HelpBackground: "#1e1e2e",
		},
		rdpOpts:   model.RDPOptions{RemotePort: 3389, Resolution: "1920x1080", Audio: "local"},
		quitAfter: true,
		states:    nil,
		conns:     map[string]model.Connection{},
		closedIDs: map[string]time.Time{},
		nameByID:  map[string]display.Named{},
	}
	return a
}

func TestViewInstancesShowsNamesAndIDs(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{
		{InstanceID: "i-0123456789abcdef0", Name: "web", State: "running", Type: "t3.micro", PrivateIP: "10.0.0.1", Platform: "Linux/UNIX"},
		{InstanceID: "i-0fedcba9876543210", Name: "web", State: "stopped", Type: "m5.large", PrivateIP: "10.0.0.2"},
	}
	a.rebuildNames("")
	view := a.View()
	if !strings.Contains(view, "web-01") || !strings.Contains(view, "web-02") {
		t.Fatalf("missing duplicate display names:\n%s", view)
	}
	for _, id := range []string{"i-0123456789abcdef0", "i-0fedcba9876543210"} {
		if !strings.Contains(view, id) {
			t.Fatalf("missing instance ID %q:\n%s", id, view)
		}
	}
	var header string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "NAME") && strings.Contains(line, "INSTANCE ID") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("missing instance ID header:\n%s", view)
	}
	stateIdx := strings.Index(header, "STATE")
	idIdx := strings.Index(header, "INSTANCE ID")
	typeIdx := strings.Index(header, "TYPE")
	if stateIdx < 0 || idIdx < 0 || typeIdx < 0 || stateIdx >= idIdx || idIdx >= typeIdx {
		t.Fatalf("columns are not ordered STATE, INSTANCE ID, TYPE: %q", header)
	}
	if !strings.Contains(view, "TUNNELS") {
		t.Fatalf("missing tunnel column:\n%s", view)
	}
	if !strings.Contains(view, "r RDP") || !strings.Contains(view, "c console") {
		t.Fatalf("missing footer:\n%s", view)
	}
}

func TestViewInstancesWideShowsAllColumns(t *testing.T) {
	a := testApp()
	a.width = 120
	a.instances = []ec2.Instance{
		{InstanceID: "i-0123456789abcdef0", Name: "web", State: "running", Type: "t3.micro", PrivateIP: "10.0.0.1", Platform: "Linux/UNIX"},
	}
	a.rebuildNames("")
	view := a.View()
	for _, header := range []string{"NAME", "STATE", "INSTANCE ID", "TYPE", "PRIVATE IP", "PLATFORM", "TUNNELS"} {
		if !strings.Contains(view, header) {
			t.Fatalf("missing %q column:\n%s", header, view)
		}
	}
}

func TestUnnamedInstanceFallsBackToID(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-abc", State: "running"}}
	a.rebuildNames("")
	if len(a.named) != 1 || a.named[0].Display != "i-abc" {
		t.Fatalf("unexpected display name: %#v", a.named)
	}
	if !strings.Contains(a.View(), "i-abc") {
		t.Fatalf("unnamed instance ID missing:\n%s", a.View())
	}
}

func TestScrollingKeepsSelectionVisible(t *testing.T) {
	a := testApp()
	a.height = 8
	for i := 0; i < 50; i++ {
		a.instances = append(a.instances, ec2.Instance{
			InstanceID: "i-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Name:       "node" + string(rune('A'+i%26)) + string(rune('a'+i/26)),
			State:      "running",
		})
	}
	a.rebuildNames("")
	a.selected = 49
	a.clampSelection()
	if a.offset == 0 {
		t.Fatal("offset did not scroll")
	}
	view := a.View()
	last := a.named[49].Display
	if !strings.Contains(view, last) {
		t.Fatalf("selected row %q not visible:\n%s", last, view)
	}
}

func TestTunnelBadgeCounts(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	a.conns["a"] = model.Connection{ID: "a", InstanceID: "i-1", State: model.StateReady}
	a.conns["b"] = model.Connection{ID: "b", InstanceID: "i-1", State: model.StateStarting}
	a.conns["c"] = model.Connection{ID: "c", InstanceID: "i-2", State: model.StateReady}
	view := a.View()
	if !strings.Contains(view, "web") {
		t.Fatalf("missing row:\n%s", view)
	}
	// The tunnel count for web should render as 2.
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "web") && strings.Contains(line, "2") {
			return
		}
	}
	t.Fatalf("tunnel badge not shown:\n%s", view)
}

func TestConnectionsViewShowsAccountAndPort(t *testing.T) {
	a := testApp()
	a.view = viewConnections
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	now := time.Now()
	a.conns["conn1"] = model.Connection{
		ID: "conn1", AccountID: "123456789012", InstanceID: "i-1", InstanceName: "web",
		LocalPort: 50123, State: model.StateReady, StartedAt: now,
	}
	a.rebuildConnOrder("conn1")
	view := a.View()
	for _, want := range []string{"123456789012", "50123", "ready", "web"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in:\n%s", want, view)
		}
	}
}

func TestReadyEventQuitsAfterRDP(t *testing.T) {
	a := testApp()
	a.pendingReady = true
	c := model.Connection{ID: "c1", InstanceID: "i-1", InstanceName: "web", LocalPort: 50000, State: model.StateReady, RDPOpened: true}
	a.handleEvent(ipc.Event{Event: "connection.updated", Connection: &c})
	if !a.quit {
		t.Fatal("expected quit after successful RDP launch")
	}
	if !strings.Contains(a.summary, "50000") || !strings.Contains(a.summary, "web") {
		t.Fatalf("unexpected summary %q", a.summary)
	}
}

func TestReadyEventStaysOpenOnOpenerFailure(t *testing.T) {
	a := testApp()
	a.pendingReady = true
	c := model.Connection{ID: "c1", InstanceID: "i-1", InstanceName: "web", LocalPort: 50000, State: model.StateReady, RDPError: "no handler", RDPPath: "/tmp/x.rdp"}
	a.handleEvent(ipc.Event{Event: "connection.updated", Connection: &c})
	if a.quit {
		t.Fatal("should stay open when opening the .rdp failed")
	}
	if !a.statusErr || !strings.Contains(a.status, "no handler") {
		t.Fatalf("missing status: %q", a.status)
	}
}

func TestViewLinesDoNotExceedWidth(t *testing.T) {
	a := testApp()
	a.width = 40
	a.height = 10
	for i := 0; i < 5; i++ {
		a.instances = append(a.instances, ec2.Instance{
			InstanceID: "i-1", Name: "a-very-long-instance-name-that-will-not-fit",
			State: "running", Type: "t3.micro", PrivateIP: "10.0.0.1", Platform: "Linux/UNIX",
		})
	}
	a.rebuildNames("")
	for _, line := range strings.Split(a.View(), "\n") {
		if w := lipgloss.Width(line); w > a.width {
			t.Fatalf("line width %d > %d: %q", w, a.width, line)
		}
	}
}

func TestRdpRefusedForNonWindows(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-linux", Name: "linux", State: "running", Platform: "Linux/UNIX"}}
	a.rebuildNames("")
	_, cmd := a.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil {
		t.Fatal("RDP connect must not start for a non-Windows instance")
	}
	if !a.statusErr || !strings.Contains(a.status, "Windows") || !strings.Contains(a.status, "console") {
		t.Fatalf("unexpected status: %q (err=%v)", a.status, a.statusErr)
	}
	if a.pendingReady {
		t.Fatal("pendingReady should remain false")
	}
}

func TestConnectCmdGuardsNonWindows(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-linux", Name: "linux", State: "running", Platform: "Linux/UNIX"}}
	a.rebuildNames("")
	cmd := a.connectCmd()
	if cmd == nil {
		t.Fatal("expected a refusal message command")
	}
	msg, ok := cmd().(connectResultMsg)
	if !ok || msg.err == nil || !strings.Contains(msg.err.Error(), "Windows") {
		t.Fatalf("unexpected result: %#v", msg)
	}
}

func TestConsoleRequiresSession(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-linux", Name: "linux", State: "running", Platform: "Linux/UNIX"}}
	a.rebuildNames("")
	_, cmd := a.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if cmd != nil {
		t.Fatal("console must not start without an AWS session")
	}
	if !a.statusErr || !strings.Contains(a.status, "unavailable") {
		t.Fatalf("unexpected status: %q", a.status)
	}
	if a.consolePending {
		t.Fatal("consolePending should remain false")
	}
}

func TestPrepareConsoleCmd(t *testing.T) {
	t.Setenv("GO_AWS_AWS_BIN", "fake-aws")
	a := testApp()
	a.ctx = context.Background()
	a.region = "us-east-1"
	a.session = &awsconfig.Session{
		Region: "us-east-1",
		Config: aws.Config{
			Region: "us-east-1",
			Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK", SessionToken: "ST"}, nil
			}),
		},
	}
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running", Platform: "Linux/UNIX"}}
	a.rebuildNames("")

	cmd := a.prepareConsoleCmd()
	if cmd == nil {
		t.Fatal("expected a console command")
	}
	msg, ok := cmd().(consoleReadyMsg)
	if !ok {
		t.Fatalf("unexpected message: %#v", msg)
	}
	if msg.err != nil {
		t.Fatalf("unexpected error: %v", msg.err)
	}
	if got, want := strings.Join(msg.cmd.Args, " "), "fake-aws ssm start-session --target i-1 --region us-east-1"; got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
	env := strings.Join(msg.cmd.Env, "\n")
	for _, want := range []string{
		"AWS_ACCESS_KEY_ID=AK",
		"AWS_SECRET_ACCESS_KEY=SK",
		"AWS_SESSION_TOKEN=ST",
		"AWS_REGION=us-east-1",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %q in console env", want)
		}
	}
}

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestStartKeyStartsStoppedInstance(t *testing.T) {
	a := testApp()
	a.session = &awsconfig.Session{}
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "stopped"}}
	a.rebuildNames("")

	_, cmd := a.handleKey(runeKey('s'))
	if cmd == nil {
		t.Fatal("start command was not created")
	}
	if !a.actionPending {
		t.Fatal("actionPending should be set")
	}
	if !strings.Contains(a.status, "Starting web") {
		t.Fatalf("unexpected status: %q", a.status)
	}
	if a.statusErr {
		t.Fatalf("start status should not be an error: %q", a.status)
	}
}

func TestStopRequiresConfirmationAndWarnsAboutTunnels(t *testing.T) {
	a := testApp()
	a.session = &awsconfig.Session{}
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	a.conns["c1"] = model.Connection{ID: "c1", InstanceID: "i-1", State: model.StateReady}

	_, cmd := a.handleKey(runeKey('S'))
	if cmd != nil {
		t.Fatal("first S must not stop the instance")
	}
	if a.confirmStopID != "i-1" {
		t.Fatalf("confirmStopID = %q, want i-1", a.confirmStopID)
	}
	if a.actionPending {
		t.Fatal("actionPending should remain false while confirming")
	}
	if !strings.Contains(a.status, "1 active tunnel") || !strings.Contains(a.status, "Press S again") {
		t.Fatalf("missing tunnel warning in status: %q", a.status)
	}

	_, cmd = a.handleKey(runeKey('S'))
	if cmd == nil {
		t.Fatal("second S did not create a stop command")
	}
	if a.confirmStopID != "" {
		t.Fatalf("confirmStopID should be cleared, got %q", a.confirmStopID)
	}
	if !a.actionPending {
		t.Fatal("actionPending should be set after confirmation")
	}
	if !strings.Contains(a.status, "Stopping web") {
		t.Fatalf("unexpected status: %q", a.status)
	}
}

func TestStopConfirmationCancels(t *testing.T) {
	for _, cancel := range []tea.KeyMsg{{Type: tea.KeyEsc}, runeKey('n')} {
		a := testApp()
		a.session = &awsconfig.Session{}
		a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
		a.rebuildNames("")

		a.handleKey(runeKey('S'))
		if a.confirmStopID == "" {
			t.Fatal("expected a pending stop confirmation")
		}
		_, cmd := a.handleKey(cancel)
		if cmd != nil {
			t.Fatal("cancel must not create a command")
		}
		if a.confirmStopID != "" {
			t.Fatalf("confirmStopID not cleared after %q", cancel.String())
		}
		if !strings.Contains(a.status, "Stop cancelled") {
			t.Fatalf("unexpected status after %q: %q", cancel.String(), a.status)
		}
	}
}

func TestInstanceActionStateGuards(t *testing.T) {
	cases := []struct {
		name  string
		state string
		key   rune
		want  string
	}{
		{"start running", "running", 's', "already running"},
		{"stop stopped", "stopped", 'S', "already stopped"},
		{"start pending", "pending", 's', "still starting"},
		{"stop stopping", "stopping", 'S', "still stopping"},
		{"start terminated", "terminated", 's', "terminated"},
		{"stop shutting down", "shutting-down", 'S', "shutting down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp()
			a.session = &awsconfig.Session{}
			a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: tc.state}}
			a.rebuildNames("")

			_, cmd := a.handleKey(runeKey(tc.key))
			if cmd != nil {
				t.Fatal("guard should not create a command")
			}
			if !a.statusErr || !strings.Contains(a.status, tc.want) {
				t.Fatalf("status = %q (err=%v), want %q", a.status, a.statusErr, tc.want)
			}
			if a.actionPending || a.confirmStopID != "" {
				t.Fatalf("guard left action state behind: pending=%v confirm=%q", a.actionPending, a.confirmStopID)
			}
		})
	}
}

func TestInstanceActionAllowsOneAtATime(t *testing.T) {
	a := testApp()
	a.session = &awsconfig.Session{}
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	a.actionPending = true

	_, cmd := a.handleKey(runeKey('S'))
	if cmd != nil {
		t.Fatal("action must not start while another is pending")
	}
	if a.confirmStopID != "" {
		t.Fatal("action must not enter stop confirmation while another is pending")
	}
	if !a.statusErr || !strings.Contains(a.status, "already in progress") {
		t.Fatalf("unexpected status: %q", a.status)
	}
}

func TestConsoleAndRDPRequireRunning(t *testing.T) {
	a := testApp()
	a.session = &awsconfig.Session{}
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "win", State: "stopped", Platform: "Windows"}}
	a.rebuildNames("")

	for _, key := range []rune{'c', 'r'} {
		a.clearStatus()
		_, cmd := a.handleKey(runeKey(key))
		if cmd != nil {
			t.Fatalf("%q created a command for a stopped instance", key)
		}
		if !a.statusErr || !strings.Contains(a.status, "press s") {
			t.Fatalf("%q status = %q (err=%v), want a press s hint", key, a.status, a.statusErr)
		}
	}
}

func TestInstanceActionResultRefreshesAndPreservesSelection(t *testing.T) {
	a := testApp()
	a.session = &awsconfig.Session{}
	a.instances = []ec2.Instance{
		{InstanceID: "i-1", Name: "web", State: "stopped"},
		{InstanceID: "i-2", Name: "db", State: "running"},
	}
	a.rebuildNames("i-2")
	a.actionPending = true

	_, cmd := a.Update(instanceActionResultMsg{action: actionStart, id: "i-1", name: "web"})
	if cmd == nil {
		t.Fatal("expected an immediate refresh")
	}
	if a.actionPending {
		t.Fatal("actionPending should be cleared")
	}
	if !strings.Contains(a.status, "Start requested for web") {
		t.Fatalf("unexpected status: %q", a.status)
	}

	a.Update(instancesMsg{
		instances: []ec2.Instance{
			{InstanceID: "i-2", Name: "db", State: "running"},
			{InstanceID: "i-1", Name: "web", State: "pending"},
		},
		note: "Start requested for web",
	})
	if got := a.named[a.selected].InstanceID; got != "i-2" {
		t.Fatalf("selection moved to %q, want i-2", got)
	}
	if !strings.Contains(a.status, "Start requested for web") {
		t.Fatalf("note was overwritten: %q", a.status)
	}
}

func TestInstanceActionErrorKeepsSelection(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	a.actionPending = true

	_, cmd := a.Update(instanceActionResultMsg{action: actionStop, id: "i-1", name: "web", err: context.DeadlineExceeded})
	if cmd != nil {
		t.Fatal("failed action must not refresh")
	}
	if a.actionPending {
		t.Fatal("actionPending should be cleared")
	}
	if !a.statusErr || !strings.Contains(a.status, "Could not stop web") {
		t.Fatalf("unexpected status: %q (err=%v)", a.status, a.statusErr)
	}
}

func TestEmptyInstanceListMessage(t *testing.T) {
	a := testApp()
	if view := a.View(); !strings.Contains(view, "No instances found.") {
		t.Fatalf("missing all-state empty message:\n%s", view)
	}

	a.states = []string{"running", "stopped"}
	view := a.View()
	if !strings.Contains(view, "No instances found in state(s): running, stopped") {
		t.Fatalf("missing filtered empty message:\n%s", view)
	}
}

func TestInfoKeyShowsInstanceDetailsAndTags(t *testing.T) {
	a := testApp()
	a.width = 100
	a.height = 40
	launch := time.Date(2026, 10, 29, 12, 34, 56, 0, time.UTC)
	a.instances = []ec2.Instance{{
		InstanceID:       "i-0123456789abcdef0",
		Name:             "web",
		State:            "running",
		Type:             "t3.micro",
		PrivateIP:        "10.0.0.1",
		Platform:         "Linux/UNIX",
		AvailabilityZone: "us-east-1a",
		LaunchTime:       launch,
		Tags: []ec2.Tag{
			{Key: "Owner", Value: "ops"},
			{Key: "Name", Value: "web"},
			{Key: "Environment", Value: "prod"},
		},
	}}
	a.rebuildNames("")

	_, cmd := a.handleKey(runeKey('i'))
	if cmd != nil {
		t.Fatal("opening instance info must not create a command")
	}
	if a.view != viewInstanceInfo {
		t.Fatalf("view = %v, want instance info", a.view)
	}
	if a.infoOffset != 0 {
		t.Fatalf("infoOffset = %d, want 0", a.infoOffset)
	}

	view := a.View()
	for _, want := range []string{
		"INSTANCE INFO", "TAGS (3)", "KEY", "VALUE",
		"i-0123456789abcdef0", "running", "t3.micro", "10.0.0.1",
		"Linux/UNIX", "us-east-1a", formatLaunchTime(launch),
		"Environment", "prod", "Name", "web", "Owner", "ops",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in info view:\n%s", want, view)
		}
	}
	lines := a.instanceInfoLines(a.contentWidth())
	tagStart := -1
	for i, line := range lines {
		if strings.Contains(line, "TAGS (3)") {
			tagStart = i
			break
		}
	}
	if tagStart < 0 {
		t.Fatalf("tags section missing:\n%s", strings.Join(lines, "\n"))
	}
	idx := map[string]int{}
	for i, line := range lines[tagStart+1:] {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			idx[fields[0]] = i
		}
	}
	if !(idx["Environment"] < idx["Name"] && idx["Name"] < idx["Owner"]) {
		t.Fatalf("tags are not alphabetical:\n%s", strings.Join(lines, "\n"))
	}
}

func TestInfoTagsUseAlignedColumns(t *testing.T) {
	a := testApp()
	a.width = 100
	a.height = 40
	a.instances = []ec2.Instance{{
		InstanceID: "i-1",
		Name:       "web",
		State:      "running",
		Tags: []ec2.Tag{
			{Key: "Owner", Value: "ops"},
			{Key: "Name", Value: "web"},
			{Key: "Environment", Value: "prod"},
		},
	}}
	a.rebuildNames("")
	lines := a.instanceInfoLines(a.contentWidth())
	headerIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "KEY") && strings.Contains(line, "VALUE") {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		t.Fatalf("tag header missing:\n%s", strings.Join(lines, "\n"))
	}
	keyPos := strings.Index(lines[headerIdx], "KEY")
	valuePos := strings.Index(lines[headerIdx], "VALUE")
	if keyPos < 0 || valuePos < 0 {
		t.Fatalf("unexpected tag header: %q", lines[headerIdx])
	}
	for _, want := range []struct{ key, value string }{
		{"Environment", "prod"},
		{"Name", "web"},
		{"Owner", "ops"},
	} {
		found := false
		for _, line := range lines[headerIdx+1:] {
			fields := strings.Fields(line)
			if len(fields) == 0 || fields[0] != want.key {
				continue
			}
			found = true
			if got := strings.Index(line, want.key); got != keyPos {
				t.Fatalf("key %q starts at %d, want %d: %q", want.key, got, keyPos, line)
			}
			if got := strings.Index(line, want.value); got != valuePos {
				t.Fatalf("value %q starts at %d, want %d: %q", want.value, got, valuePos, line)
			}
		}
		if !found {
			t.Fatalf("tag row %q missing:\n%s", want.key, strings.Join(lines, "\n"))
		}
	}
}

func TestInfoBackPreservesSelection(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{
		{InstanceID: "i-1", Name: "one", State: "running"},
		{InstanceID: "i-2", Name: "two", State: "running"},
	}
	a.rebuildNames("")
	a.selected = 1

	a.handleKey(runeKey('i'))
	if a.view != viewInstanceInfo {
		t.Fatal("i did not open instance info")
	}
	_, cmd := a.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("esc must not create a command")
	}
	if a.view != viewInstances {
		t.Fatalf("view = %v, want instances", a.view)
	}
	if a.selected != 1 {
		t.Fatalf("selection = %d, want 1", a.selected)
	}

	a.handleKey(runeKey('i'))
	a.handleKey(runeKey('i'))
	if a.view != viewInstances {
		t.Fatalf("second i did not return to instances: %v", a.view)
	}
}

func TestInfoOpensFromHelpRoundTrip(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")

	a.handleKey(runeKey('i'))
	a.handleKey(runeKey('?'))
	if a.view != viewHelp {
		t.Fatalf("view = %v, want help", a.view)
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewInstanceInfo {
		t.Fatalf("help did not return to info: %v", a.view)
	}
}

func TestInfoDoesNothingWithoutInstances(t *testing.T) {
	a := testApp()
	_, cmd := a.handleKey(runeKey('i'))
	if cmd != nil {
		t.Fatal("i must not create a command")
	}
	if a.view != viewInstances {
		t.Fatalf("view = %v, want instances", a.view)
	}
}

func TestTagsAreSortedAndSanitized(t *testing.T) {
	got := sortedTags([]ec2.Tag{
		{Key: "zeta", Value: "last"},
		{Key: "Alpha", Value: "\x1b[31mred\x1b[0m"},
		{Key: "beta", Value: "line\nbreak"},
	})
	if len(got) != 3 {
		t.Fatalf("got %d tags", len(got))
	}
	if got[0].Key != "Alpha" || got[1].Key != "beta" || got[2].Key != "zeta" {
		t.Fatalf("unexpected order: %+v", got)
	}
	if got[0].Value != "red" {
		t.Fatalf("tag value was not sanitized: %q", got[0].Value)
	}
	if got[1].Value != "line break" {
		t.Fatalf("tag newline was not normalized: %q", got[1].Value)
	}
}

func TestInfoScrollsLongTagList(t *testing.T) {
	a := testApp()
	a.width = 80
	a.height = 12
	inst := ec2.Instance{InstanceID: "i-1", Name: "web", State: "running"}
	for i := 0; i < 30; i++ {
		inst.Tags = append(inst.Tags, ec2.Tag{Key: fmt.Sprintf("T%02d", i), Value: "value"})
	}
	a.instances = []ec2.Instance{inst}
	a.rebuildNames("")
	a.handleKey(runeKey('i'))

	if strings.Contains(a.View(), "T29") {
		t.Fatal("last tag should be below the initial viewport")
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if a.infoOffset == 0 {
		t.Fatal("End did not scroll the info view")
	}
	if !strings.Contains(a.View(), "T29") {
		t.Fatalf("last tag not visible after End:\n%s", a.View())
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyHome})
	if a.infoOffset != 0 {
		t.Fatalf("Home did not reset scrolling: %d", a.infoOffset)
	}
	for _, line := range strings.Split(a.View(), "\n") {
		if w := lipgloss.Width(line); w > a.width {
			t.Fatalf("line width %d > %d: %q", w, a.width, line)
		}
	}
}

func TestInfoWrapsLongTagValue(t *testing.T) {
	a := testApp()
	a.width = 50
	a.height = 40
	a.instances = []ec2.Instance{{
		InstanceID: "i-1",
		Name:       "web",
		State:      "running",
		Tags:       []ec2.Tag{{Key: "Description", Value: "one two three four five six seven eight nine ten"}},
	}}
	a.rebuildNames("")
	a.handleKey(runeKey('i'))
	view := a.View()
	if !strings.Contains(view, "Description  one two") {
		t.Fatalf("tag value not rendered:\n%s", view)
	}
	if !strings.Contains(view, "nine ten") && !strings.Contains(view, "eight nine") {
		t.Fatalf("long tag value was not wrapped:\n%s", view)
	}
	lines := a.instanceInfoLines(a.contentWidth())
	valuePos := -1
	for _, line := range lines {
		if strings.Contains(line, "KEY") && strings.Contains(line, "VALUE") {
			valuePos = strings.Index(line, "VALUE")
			break
		}
	}
	if valuePos < 0 {
		t.Fatalf("tag header missing:\n%s", strings.Join(lines, "\n"))
	}
	wrapped := false
	for _, line := range lines {
		if strings.Contains(line, "seven eight") {
			wrapped = true
			if got := strings.Index(line, "seven eight"); got != valuePos {
				t.Fatalf("wrapped value starts at %d, want %d: %q", got, valuePos, line)
			}
		}
	}
	if !wrapped {
		t.Fatalf("long tag value was not wrapped in a column:\n%s", strings.Join(lines, "\n"))
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > a.width {
			t.Fatalf("line width %d > %d: %q", w, a.width, line)
		}
	}
}

func TestInfoTruncatesLongTagKey(t *testing.T) {
	a := testApp()
	a.width = 40
	a.height = 40
	longKey := strings.Repeat("K", 80)
	a.instances = []ec2.Instance{{
		InstanceID: "i-1",
		Name:       "web",
		State:      "running",
		Tags:       []ec2.Tag{{Key: longKey, Value: "value"}},
	}}
	a.rebuildNames("")
	lines := a.instanceInfoLines(a.contentWidth())
	valuePos := -1
	for _, line := range lines {
		if strings.Contains(line, "KEY") && strings.Contains(line, "VALUE") {
			valuePos = strings.Index(line, "VALUE")
			break
		}
	}
	if valuePos < 0 {
		t.Fatalf("tag header missing:\n%s", strings.Join(lines, "\n"))
	}
	found := false
	for _, line := range lines {
		if !strings.Contains(line, "…") || !strings.Contains(line, "value") {
			continue
		}
		found = true
		got := strings.Index(line, "value")
		if got < 0 || lipgloss.Width(line[:got]) != valuePos {
			t.Fatalf("value starts at column %d, want %d: %q", lipgloss.Width(line[:got]), valuePos, line)
		}
	}
	if !found {
		t.Fatalf("long key was not truncated into a tag row:\n%s", strings.Join(lines, "\n"))
	}
}

func TestEnterNoLongerOpensConsole(t *testing.T) {
	a := testApp()
	a.session = &awsconfig.Session{}
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running", Platform: "Linux/UNIX"}}
	a.rebuildNames("")

	_, cmd := a.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter must not create a console command")
	}
	if a.consolePending {
		t.Fatal("Enter must not start console preparation")
	}
	if a.status != "" {
		t.Fatalf("Enter unexpectedly set status %q", a.status)
	}

	_, cmd = a.handleKey(runeKey('c'))
	if cmd == nil || !a.consolePending {
		t.Fatal("c must still start the console action")
	}
}

func TestHelpAndFooterMentionInfoAndNotEnterConsole(t *testing.T) {
	a := testApp()
	a.height = 40
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	if !strings.Contains(a.View(), "i info") {
		t.Fatalf("footer missing i info:\n%s", a.View())
	}
	a.view = viewHelp
	help := a.View()
	if !strings.Contains(help, "i info") {
		t.Fatalf("help missing i shortcut:\n%s", help)
	}
	if strings.Contains(help, "c / enter") {
		t.Fatalf("help still lists enter for console:\n%s", help)
	}
}

func TestInfoShowsNoTagsWhenEmpty(t *testing.T) {
	a := testApp()
	a.height = 40
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	a.handleKey(runeKey('i'))
	view := a.View()
	if !strings.Contains(view, "TAGS (0)") || !strings.Contains(view, "No tags") {
		t.Fatalf("missing empty tags message:\n%s", view)
	}
}

func typeRunes(a *App, s string) {
	for _, r := range s {
		a.handleKey(runeKey(r))
	}
}

func TestHelpFitsStandardTerminal(t *testing.T) {
	a := testApp()
	a.width = 80
	a.height = 24
	a.view = viewHelp
	view := a.View()
	if got := len(strings.Split(view, "\n")); got != a.height {
		t.Fatalf("help rendered %d lines, want %d", got, a.height)
	}
	for _, want := range []string{"FILTER", "CONFIG", "e edit the config file", "Press esc"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help is missing %q:\n%s", want, view)
		}
	}
}

func TestFooterIncludesFilterAndEditorAtEightyColumns(t *testing.T) {
	a := testApp()
	a.width = 80
	a.height = 24
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	lines := strings.Split(a.View(), "\n")
	footer := lines[len(lines)-2]
	for _, want := range []string{"/ filter", "e edit", "i info", " q"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer missing %q: %q", want, footer)
		}
	}
}

func TestStatusBarIsLastRenderedLine(t *testing.T) {
	a := testApp()
	a.accountID = "123456789012"
	a.region = "us-east-1"
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	lines := strings.Split(a.View(), "\n")
	if len(lines) != a.height {
		t.Fatalf("rendered %d lines, want %d", len(lines), a.height)
	}
	if !strings.Contains(lines[len(lines)-1], "go-aws") || !strings.Contains(lines[len(lines)-1], "us-east-1") {
		t.Fatalf("status bar is not the last line: %q", lines[len(lines)-1])
	}
	if strings.Contains(lines[0], "go-aws") {
		t.Fatalf("status bar still rendered at the top: %q", lines[0])
	}
}

func TestFilterMatchesEveryInstanceColumnLive(t *testing.T) {
	for _, query := range []string{"WEB", "running", "I-0001", "T3.MICRO", "10.2.3.4", "linux"} {
		t.Run(query, func(t *testing.T) {
			a := testApp()
			a.instances = []ec2.Instance{{
				InstanceID: "i-0001", Name: "web", State: "running",
				Type: "t3.micro", PrivateIP: "10.2.3.4", Platform: "Linux/UNIX",
			}}
			a.rebuildNames("")
			a.handleKey(runeKey('/'))
			if !a.filterEditing {
				t.Fatal("/ did not enter filter mode")
			}
			typeRunes(a, query)
			if got := a.filterQueries[0]; got != query {
				t.Fatalf("filter = %q, want %q", got, query)
			}
			if len(a.visibleNamed) != 1 || a.visibleNamed[0].InstanceID != "i-0001" {
				t.Fatalf("visible rows = %+v", a.visibleNamed)
			}
			if !strings.Contains(a.View(), "1/1 matches") {
				t.Fatalf("missing live match count:\n%s", a.View())
			}
			a.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
			if a.filterEditing {
				t.Fatal("enter did not leave filter mode")
			}
		})
	}
}

func TestFilterMatchesTunnelCount(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{
		InstanceID: "i-abcdef", Name: "web", State: "running",
		Type: "m5.large", PrivateIP: "10.0.0.1", Platform: "Linux/UNIX",
	}}
	a.rebuildNames("")
	for _, id := range []string{"c1", "c2", "c3"} {
		a.conns[id] = model.Connection{ID: id, InstanceID: "i-abcdef", State: model.StateReady}
	}
	a.handleKey(runeKey('/'))
	typeRunes(a, "3")
	if len(a.visibleNamed) != 1 {
		t.Fatalf("tunnel count was not searchable: %+v", a.visibleNamed)
	}
}

func TestFilterBackspaceAndCtrlU(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	a.handleKey(runeKey('/'))
	typeRunes(a, "web")
	if got := a.filterQueries[0]; got != "web" {
		t.Fatalf("filter = %q", got)
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := a.filterQueries[0]; got != "we" {
		t.Fatalf("backspace filter = %q", got)
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := a.filterQueries[0]; got != "" || len(a.visibleNamed) != 1 {
		t.Fatalf("ctrl+u filter = %q, visible=%d", got, len(a.visibleNamed))
	}
}

func TestFilterEnterKeepsAndEscRestores(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{
		{InstanceID: "i-1", Name: "web", State: "running"},
		{InstanceID: "i-2", Name: "db", State: "running"},
	}
	a.rebuildNames("")
	a.handleKey(runeKey('/'))
	typeRunes(a, "web")
	a.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.filterEditing || a.filterQueries[0] != "web" || len(a.visibleNamed) != 1 {
		t.Fatalf("enter did not keep filter: editing=%v query=%q visible=%d", a.filterEditing, a.filterQueries[0], len(a.visibleNamed))
	}
	a.handleKey(runeKey('/'))
	a.handleKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	typeRunes(a, "db")
	if len(a.visibleNamed) != 1 || a.visibleNamed[0].Display != "db" {
		t.Fatalf("live filter did not update: %+v", a.visibleNamed)
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.filterEditing || a.filterQueries[0] != "web" {
		t.Fatalf("esc did not restore previous filter: editing=%v query=%q", a.filterEditing, a.filterQueries[0])
	}
	if len(a.visibleNamed) != 1 || a.visibleNamed[0].Display != "web" {
		t.Fatalf("restored filter did not apply: %+v", a.visibleNamed)
	}
}

func TestEscInNormalModeClearsFilter(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{
		{InstanceID: "i-1", Name: "web", State: "running"},
		{InstanceID: "i-2", Name: "db", State: "running"},
	}
	a.rebuildNames("")
	a.handleKey(runeKey('/'))
	typeRunes(a, "web")
	a.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	a.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.filterQueries[0] != "" || len(a.visibleNamed) != 2 {
		t.Fatalf("esc did not clear filter: query=%q visible=%d", a.filterQueries[0], len(a.visibleNamed))
	}
}

func TestFilterKeepsSelectionWhenStillVisible(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{
		{InstanceID: "i-1", Name: "api", State: "running"},
		{InstanceID: "i-2", Name: "db", State: "running"},
		{InstanceID: "i-3", Name: "web", State: "running"},
	}
	a.rebuildNames("")
	a.selected = 1
	a.clampSelection()
	a.handleKey(runeKey('/'))
	typeRunes(a, "db")
	if got := a.selectedInstanceID(); got != "i-2" {
		t.Fatalf("selection = %q, want i-2", got)
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	typeRunes(a, "web")
	if got := a.selectedInstanceID(); got != "i-3" {
		t.Fatalf("selection = %q, want first match i-3", got)
	}
}

func TestFilterIsPerTableView(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{
		{InstanceID: "i-1", Name: "web", State: "running"},
		{InstanceID: "i-2", Name: "db", State: "running"},
	}
	a.rebuildNames("")
	a.conns["c1"] = model.Connection{ID: "c1", InstanceID: "i-1", InstanceName: "web", LocalPort: 50123, State: model.StateReady, StartedAt: time.Now()}
	a.conns["c2"] = model.Connection{ID: "c2", InstanceID: "i-2", InstanceName: "db", LocalPort: 50124, State: model.StateReady, StartedAt: time.Now()}
	a.rebuildConnOrder("")

	a.handleKey(runeKey('/'))
	typeRunes(a, "web")
	a.handleKey(tea.KeyMsg{Type: tea.KeyEnter})

	a.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.view != viewConnections {
		t.Fatal("tab did not switch to connections")
	}
	if len(a.visibleConns) != 2 {
		t.Fatalf("connections should not inherit the instance filter: %d", len(a.visibleConns))
	}
	a.handleKey(runeKey('/'))
	typeRunes(a, "50123")
	a.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if len(a.visibleConns) != 1 || a.visibleConns[0] != "c1" {
		t.Fatalf("connection filter = %+v", a.visibleConns)
	}

	a.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if a.filterQueries[0] != "web" || len(a.visibleNamed) != 1 {
		t.Fatalf("instance filter was lost: query=%q visible=%d", a.filterQueries[0], len(a.visibleNamed))
	}
}

func TestFilterMatchesConnectionColumns(t *testing.T) {
	for _, query := range []string{"WEB", "123456789012", "50123", "READY", "CONN-ALPHA"} {
		t.Run(query, func(t *testing.T) {
			a := testApp()
			a.view = viewConnections
			a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
			a.rebuildNames("")
			a.conns["conn-alpha"] = model.Connection{
				ID: "conn-alpha", AccountID: "123456789012", InstanceID: "i-1",
				InstanceName: "web", LocalPort: 50123, State: model.StateReady, StartedAt: time.Now(),
			}
			a.rebuildConnOrder("")
			a.handleKey(runeKey('/'))
			typeRunes(a, query)
			if len(a.visibleConns) != 1 || a.visibleConns[0] != "conn-alpha" {
				t.Fatalf("visible connections = %+v", a.visibleConns)
			}
			a.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
		})
	}
}

func TestFilterZeroMatchesMessage(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{{InstanceID: "i-1", Name: "web", State: "running"}}
	a.rebuildNames("")
	a.handleKey(runeKey('/'))
	typeRunes(a, "missing")
	view := a.View()
	if !strings.Contains(view, `No instances match "missing".`) {
		t.Fatalf("missing zero-match message:\n%s", view)
	}
	if !strings.Contains(view, "0/1 matches") {
		t.Fatalf("missing zero-match count:\n%s", view)
	}
}

func TestFilterSearchesHiddenColumns(t *testing.T) {
	a := testApp()
	a.width = 40
	a.instances = []ec2.Instance{{
		InstanceID: "i-1", Name: "web", State: "running",
		Type: "t3.micro", PrivateIP: "10.0.0.1", Platform: "Linux/UNIX",
	}}
	a.rebuildNames("")
	if strings.Contains(a.View(), "PLATFORM") {
		t.Skip("platform column is visible at this width")
	}
	a.handleKey(runeKey('/'))
	typeRunes(a, "linux")
	if len(a.visibleNamed) != 1 {
		t.Fatalf("hidden platform column was not searchable: %+v", a.visibleNamed)
	}
}

func TestMouseClickMapsFilteredRows(t *testing.T) {
	a := testApp()
	a.instances = []ec2.Instance{
		{InstanceID: "i-1", Name: "web-1", State: "running"},
		{InstanceID: "i-2", Name: "web-2", State: "running"},
		{InstanceID: "i-3", Name: "db-1", State: "running"},
	}
	a.rebuildNames("")
	a.handleKey(runeKey('/'))
	typeRunes(a, "web")
	a.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	a.handleMouse(tea.MouseMsg{X: 0, Y: 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if got := a.selectedInstanceID(); got != "i-1" {
		t.Fatalf("first filtered row selected %q", got)
	}
	a.handleMouse(tea.MouseMsg{X: 0, Y: 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if got := a.selectedInstanceID(); got != "i-2" {
		t.Fatalf("second filtered row selected %q", got)
	}
}

func TestStatusTransientKeysAndHelpStylesUseConfiguredColors(t *testing.T) {
	a := testApp()
	a.theme.StatusForeground = "#010203"
	a.theme.StatusBackground = "#040506"
	a.theme.TransientForeground = "#111213"
	a.theme.TransientBackground = "#141516"
	a.theme.TransientErrorForeground = "#171819"
	a.theme.KeysForeground = "#1a1b1c"
	a.theme.KeysBackground = "#1d1e1f"
	a.theme.HelpForeground = "#202122"
	a.theme.HelpBackground = "#232425"
	st := a.styles()

	assertStyleColor(t, "status foreground", st.status.GetForeground(), "#010203")
	assertStyleColor(t, "status background", st.status.GetBackground(), "#040506")
	assertStyleColor(t, "transient foreground", st.transient.GetForeground(), "#111213")
	assertStyleColor(t, "transient background", st.transient.GetBackground(), "#141516")
	assertStyleColor(t, "transient error foreground", st.transientErr.GetForeground(), "#171819")
	assertStyleColor(t, "transient error background", st.transientErr.GetBackground(), "#141516")
	assertStyleColor(t, "keys foreground", st.keys.GetForeground(), "#1a1b1c")
	assertStyleColor(t, "keys background", st.keys.GetBackground(), "#1d1e1f")
	assertStyleColor(t, "help foreground", st.help.GetForeground(), "#202122")
	assertStyleColor(t, "help background", st.help.GetBackground(), "#232425")
}

func assertStyleColor(t *testing.T, name string, got lipgloss.TerminalColor, want string) {
	t.Helper()
	c, ok := got.(lipgloss.Color)
	if !ok || string(c) != want {
		t.Fatalf("%s = %v, want %s", name, got, want)
	}
}

func TestEditorDoneReloadsColorsAndSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	raw := `
app:
  colors:
    list_foreground: "#010203"
  editor: ["true", "{file}"]
accounts:
  "123456789012":
    colors:
      selected_background: "#040506"
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	a := testApp()
	a.accountID = "123456789012"
	a.configPath = path
	_, cmd := a.Update(editorDoneMsg{})
	if cmd != nil {
		t.Fatal("did not expect a refresh without a state change")
	}
	if a.theme.ListForeground != "#010203" || a.theme.SelectedBackground != "#040506" {
		t.Fatalf("reloaded colors = %+v", a.theme)
	}
	if got := strings.Join(a.editor, " "); got != "true {file}" {
		t.Fatalf("reloaded editor = %q", got)
	}
	if !strings.Contains(a.status, "Configuration reloaded") {
		t.Fatalf("unexpected status: %q", a.status)
	}
}

func TestEditorDoneRefreshesInstancesWhenStatesChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	raw := `
aws:
  states: [running]
app:
  colors:
    list_foreground: "#010203"
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	a := testApp()
	a.accountID = "123456789012"
	a.configPath = path
	a.states = []string{"stopped"}
	a.session = &awsconfig.Session{}
	_, cmd := a.Update(editorDoneMsg{})
	if cmd == nil {
		t.Fatal("expected an instance refresh after aws.states changed")
	}
	if got := strings.Join(a.states, ","); got != "running" {
		t.Fatalf("reloaded states = %q", got)
	}
	if a.theme.ListForeground != "#010203" {
		t.Fatalf("reloaded colors = %+v", a.theme)
	}
}

func TestEditConfigCmdBuildsEditorProcess(t *testing.T) {
	a := testApp()
	a.configPath = filepath.Join(t.TempDir(), "config.yml")
	a.editor = []string{"true", "{file}"}
	cmd := a.editConfigCmd()
	if cmd == nil {
		t.Fatal("expected an editor command")
	}
	msg, ok := cmd().(editorReadyMsg)
	if !ok || msg.cmd == nil {
		t.Fatalf("unexpected message: %#v", msg)
	}
	if got := strings.Join(msg.cmd.Args, " "); !strings.Contains(got, a.configPath) {
		t.Fatalf("editor args = %q", got)
	}
}
