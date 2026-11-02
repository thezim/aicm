package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/thezim/aicm/internal/ipc"
	"github.com/thezim/aicm/internal/platform"
)

// TryConnect dials the daemon without starting it.
func TryConnect(ctx context.Context) (*ipc.Client, error) {
	return ipc.Dial(ctx)
}

// EnsureRunning connects to the daemon, auto-starting a detached daemon if
// none is listening.
func EnsureRunning(ctx context.Context, configPath string) (*ipc.Client, error) {
	if c, err := dialDaemon(ctx); err == nil {
		return c, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	logPath, logErr := platform.LogPath()
	var logFile *os.File
	if logErr == nil {
		logFile, _ = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}

	args := []string{"daemon", "run"}
	if strings.TrimSpace(configPath) != "" {
		args = append(args, "--config", configPath)
	}
	cmd := exec.Command(exe, args...)
	platform.Detach(cmd)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.Stdin = nil
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	if logFile != nil {
		_ = logFile.Close()
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		c, err := dialDaemon(dialCtx)
		cancel()
		if err == nil {
			return c, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	hint := ""
	if logErr == nil {
		hint = fmt.Sprintf(" (see daemon log at %s)", logPath)
	}
	if lastErr == nil {
		lastErr = errors.New("endpoint not ready")
	}
	return nil, fmt.Errorf("daemon did not start: %w%s", lastErr, hint)
}

func dialDaemon(ctx context.Context) (*ipc.Client, error) {
	client, err := ipc.Dial(ctx)
	if err != nil {
		return nil, err
	}
	helloCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var hello Hello
	if err := client.Call(helloCtx, "hello", nil, &hello); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("daemon handshake: %w", err)
	}
	if hello.Protocol != ipc.ProtocolVersion {
		_ = client.Close()
		return nil, fmt.Errorf("daemon protocol %d is incompatible with client protocol %d; run `aicm daemon stop` and retry", hello.Protocol, ipc.ProtocolVersion)
	}
	return client, nil
}
