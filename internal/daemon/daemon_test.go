package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thezim/aicm/internal/ipc"
	"github.com/thezim/aicm/internal/model"
	"github.com/thezim/aicm/internal/tunnel"
)

// TestMain doubles as the fake AWS CLI and fake RDP opener used by the
// integration tests. Child processes are recognized by AICM_TEST_HELPER.
func TestMain(m *testing.M) {
	if os.Getenv("AICM_TEST_HELPER") == "1" {
		os.Exit(runFakeChild())
	}
	os.Exit(m.Run())
}

func runFakeChild() int {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "ssm" {
		if logPath := os.Getenv("AICM_TEST_ENV_LOG"); logPath != "" {
			var lines []string
			for _, kv := range os.Environ() {
				if strings.HasPrefix(kv, "AWS_") {
					lines = append(lines, kv)
				}
			}
			_ = os.WriteFile(logPath, []byte(strings.Join(lines, "\n")), 0o600)
		}
		mode := os.Getenv("AICM_TEST_AWS_MODE")
		if mode == "fail" {
			fmt.Fprintln(os.Stderr, "AccessDenied: fake ssm start-session failure")
			return 1
		}
		localPort := 0
		for i, a := range args {
			if a == "--parameters" && i+1 < len(args) {
				var params map[string][]string
				if err := json.Unmarshal([]byte(args[i+1]), &params); err == nil {
					if v := params["localPortNumber"]; len(v) > 0 {
						fmt.Sscanf(v[0], "%d", &localPort)
					}
				}
			}
		}
		if localPort == 0 {
			fmt.Fprintln(os.Stderr, "fake aws: missing local port")
			return 2
		}
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", localPort))
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake aws: listen: %v\n", err)
			return 2
		}
		defer listener.Close()
		fmt.Printf("Starting session manager plugin...\nPort %d opened for sessionId fake-session\n", localPort)
		switch mode {
		case "exit":
			time.Sleep(3 * time.Second)
			return 3
		default:
			time.Sleep(30 * time.Second)
			return 0
		}
	}
	// The opener receives the .rdp path and copies it for inspection.
	if path := os.Getenv("AICM_TEST_OPENED"); path != "" {
		for _, a := range args {
			if strings.HasSuffix(a, ".rdp") {
				data, err := os.ReadFile(a)
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
				return writeFileOrFail(path, data)
			}
		}
	}
	return 2
}

func writeFileOrFail(path string, data []byte) int {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type testEnv struct {
	cache   string
	client  *ipc.Client
	cancel  context.CancelFunc
	done    chan error
	stopped chan struct{}
	socket  string
	opened  string
	envLog  string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("AICM_CACHE_DIR", cache)
	t.Setenv("AICM_AWS_BIN", os.Args[0])
	t.Setenv("AICM_TEST_HELPER", "1")
	t.Setenv("AICM_TEST_AWS_MODE", "ok")
	opened := filepath.Join(cache, "opened.rdp")
	envLog := filepath.Join(cache, "aws-env.log")
	t.Setenv("AICM_TEST_OPENED", opened)
	t.Setenv("AICM_TEST_ENV_LOG", envLog)

	sockDir, err := os.MkdirTemp("", "aicm-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "daemon.sock")
	t.Setenv("AICM_SOCKET", sock)
	t.Setenv("AICM_PIPE", `\\.\pipe\aicm-test-`+strings.ReplaceAll(strings.TrimPrefix(sockDir, os.TempDir()), string(os.PathSeparator), "-"))

	return &testEnv{cache: cache, socket: sock, opened: opened, envLog: envLog}
}

func (e *testEnv) start(t *testing.T, opts Options) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		done <- Run(ctx, opts)
		close(stopped)
	}()
	e.cancel = cancel
	e.done = done
	e.stopped = stopped
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dialCtx, c := context.WithTimeout(context.Background(), 200*time.Millisecond)
		client, err := ipc.Dial(dialCtx)
		c()
		if err == nil {
			e.client = client
			t.Cleanup(func() {
				if e.client != nil {
					_ = e.client.Close()
				}
				cancel()
				select {
				case <-stopped:
				case <-time.After(8 * time.Second):
					t.Errorf("daemon did not stop")
				}
			})
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	t.Fatalf("daemon did not start; done=%v", <-done)
}

func (e *testEnv) connect(t *testing.T, name string) model.Connection {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var conn model.Connection
	err := e.client.Call(ctx, "connect", ConnectParams{
		AccountID:    "123456789012",
		InstanceID:   "i-test123",
		InstanceName: name,
		Platform:     "Windows",
		Region:       "us-east-1",
		RDP: model.RDPOptions{
			RemotePort: 3389,
			Resolution: "1920x1080",
			Clipboard:  true,
			Audio:      "local",
		},
		Opener: []string{os.Args[0], "{file}"},
		Credentials: model.Credentials{
			AccessKeyID:     "NEWKEY",
			SecretAccessKey: "NEWSECRET",
			SessionToken:    "NEWTOKEN",
		},
	}, &conn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return conn
}

func (e *testEnv) list(t *testing.T) []model.Connection {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var conns []model.Connection
	if err := e.client.Call(ctx, "list_connections", nil, &conns); err != nil {
		t.Fatalf("list: %v", err)
	}
	return conns
}

func (e *testEnv) waitReady(t *testing.T, id string) model.Connection {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, c := range e.list(t) {
			if c.ID == id && c.State == model.StateReady {
				return c
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("connection %s did not become ready", id)
	return model.Connection{}
}

func (e *testEnv) waitEmpty(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.list(t)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("connection was not cleaned up")
}

func TestDaemonTunnelLifecycle(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("AWS_PROFILE", "should-not-leak")
	t.Setenv("AWS_ACCESS_KEY_ID", "OLDSHOULDNOTLEAK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "OLDSHOULDNOTLEAK")
	env.start(t, Options{ConnectTimeout: 5 * time.Second, IdleExit: 300 * time.Millisecond, Version: "test"})

	conn := env.connect(t, "web")
	ready := env.waitReady(t, conn.ID)
	if ready.LocalPort == 0 {
		t.Fatal("no local port")
	}
	if !ready.RDPOpened {
		t.Fatalf("RDP opener did not report success: %+v", ready)
	}
	if ready.RDPPath == "" {
		t.Fatal("no RDP path")
	}
	raw, err := os.ReadFile(ready.RDPPath)
	if err != nil {
		t.Fatalf("read RDP file: %v", err)
	}
	if !strings.Contains(string(raw), fmt.Sprintf("full address:s:127.0.0.1:%d", ready.LocalPort)) {
		t.Fatalf("unexpected RDP content:\n%s", raw)
	}
	if _, err := os.Stat(env.opened); err != nil {
		t.Fatalf("opener was not called: %v", err)
	}
	if !tunnel.PortOpen(ready.LocalPort) {
		t.Fatalf("port %d is not open", ready.LocalPort)
	}

	awsEnv, err := os.ReadFile(env.envLog)
	if err != nil {
		t.Fatalf("read child env log: %v", err)
	}
	childEnv := string(awsEnv)
	if strings.Contains(childEnv, "OLDSHOULDNOTLEAK") || strings.Contains(childEnv, "AWS_PROFILE=") {
		t.Fatalf("inherited AWS auth leaked to child:\n%s", childEnv)
	}
	for _, want := range []string{"AWS_ACCESS_KEY_ID=NEWKEY", "AWS_SECRET_ACCESS_KEY=NEWSECRET", "AWS_SESSION_TOKEN=NEWTOKEN"} {
		if !strings.Contains(childEnv, want) {
			t.Fatalf("missing %q in child env:\n%s", want, childEnv)
		}
	}

	statePath := filepath.Join(env.cache, "daemon-state.json")
	stateRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if strings.Contains(string(stateRaw), "NEWSECRET") || strings.Contains(string(stateRaw), "NEWKEY") || strings.Contains(string(stateRaw), "NEWTOKEN") {
		t.Fatalf("credentials persisted in state:\n%s", stateRaw)
	}

	// Multiple tunnels to the same instance must get distinct ports.
	conn2 := env.connect(t, "web")
	ready2 := env.waitReady(t, conn2.ID)
	if ready2.LocalPort == ready.LocalPort {
		t.Fatalf("duplicate local port %d", ready2.LocalPort)
	}
	if len(env.list(t)) != 2 {
		t.Fatalf("expected 2 connections")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.client.Call(ctx, "close_connection", CloseParams{All: true}, nil); err != nil {
		t.Fatal(err)
	}
	env.waitEmpty(t)
	deadline := time.Now().Add(3 * time.Second)
	for tunnel.PortOpen(ready.LocalPort) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if tunnel.PortOpen(ready.LocalPort) || tunnel.PortOpen(ready2.LocalPort) {
		t.Fatal("ports still open after close")
	}
	if _, err := os.Stat(ready.RDPPath); !os.IsNotExist(err) {
		t.Fatalf("RDP file still exists after close: %v", err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state file still exists after close: %v", err)
	}
}

func TestDaemonRefusesNonWindowsRDP(t *testing.T) {
	env := newTestEnv(t)
	env.start(t, Options{ConnectTimeout: 2 * time.Second, IdleExit: time.Minute, Version: "test"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var conn model.Connection
	err := env.client.Call(ctx, "connect", ConnectParams{
		AccountID:    "123456789012",
		InstanceID:   "i-linux",
		InstanceName: "linux",
		Platform:     "Linux/UNIX",
		Region:       "us-east-1",
		RDP:          model.RDPOptions{RemotePort: 3389, Resolution: "1920x1080", Audio: "local"},
		Opener:       []string{os.Args[0], "{file}"},
		Credentials:  model.Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"},
	}, &conn)
	if err == nil || !strings.Contains(err.Error(), "Windows") {
		t.Fatalf("expected Windows-only refusal, got %v", err)
	}
	if len(env.list(t)) != 0 {
		t.Fatal("refused connection must not be tracked")
	}
	if _, statErr := os.Stat(filepath.Join(env.cache, "rdp")); statErr == nil {
		entries, _ := os.ReadDir(filepath.Join(env.cache, "rdp"))
		if len(entries) != 0 {
			t.Fatalf("RDP files created for refused connection: %v", entries)
		}
	}
}

func TestDaemonFailureCleansUp(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("AICM_TEST_AWS_MODE", "fail")
	env.start(t, Options{ConnectTimeout: 3 * time.Second, IdleExit: time.Minute, Version: "test"})
	conn := env.connect(t, "broken")
	env.waitEmpty(t)
	if conn.LocalPort > 0 && tunnel.PortOpen(conn.LocalPort) {
		t.Fatalf("failed tunnel left port %d open", conn.LocalPort)
	}
	if _, err := os.Stat(env.opened); !os.IsNotExist(err) {
		t.Fatalf("opener ran for a failed tunnel")
	}
	if _, err := os.Stat(filepath.Join(env.cache, "rdp")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(env.cache, "rdp"))
		if len(entries) != 0 {
			t.Fatalf("RDP files left behind: %v", entries)
		}
	}
}

func TestDaemonProcessExitCleansUp(t *testing.T) {
	env := newTestEnv(t)
	t.Setenv("AICM_TEST_AWS_MODE", "exit")
	env.start(t, Options{ConnectTimeout: 5 * time.Second, IdleExit: time.Minute, Version: "test"})
	conn := env.connect(t, "flaky")
	ready := env.waitReady(t, conn.ID)
	env.waitEmpty(t)
	deadline := time.Now().Add(3 * time.Second)
	for tunnel.PortOpen(ready.LocalPort) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if tunnel.PortOpen(ready.LocalPort) {
		t.Fatal("port still open after AWS CLI exit")
	}
	if _, err := os.Stat(ready.RDPPath); !os.IsNotExist(err) {
		t.Fatalf("RDP file still exists after AWS CLI exit: %v", err)
	}
}

func TestDaemonIdleExit(t *testing.T) {
	env := newTestEnv(t)
	env.start(t, Options{ConnectTimeout: 3 * time.Second, IdleExit: 200 * time.Millisecond, Version: "test"})
	_ = env.client.Close()
	env.client = nil
	select {
	case <-env.stopped:
		select {
		case err := <-env.done:
			if err != nil {
				t.Fatalf("daemon exited with error: %v", err)
			}
		default:
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not exit when idle")
	}
}

func TestDaemonStaleStateReconciliation(t *testing.T) {
	// A state entry with a dead PID must be removed without signaling anything.
	cache := t.TempDir()
	t.Setenv("AICM_CACHE_DIR", cache)
	state := stateFile{Version: 1, Entries: []stateEntry{{PID: 999999, InstanceID: "i-old", LocalPort: 44444, RDPPath: filepath.Join(cache, "missing.rdp")}}}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(cache, "daemon-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	CleanupStale()
	if _, err := os.Stat(filepath.Join(cache, "daemon-state.json")); !os.IsNotExist(err) {
		t.Fatalf("stale state not removed: %v", err)
	}
}
