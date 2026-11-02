//go:build !windows

package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ErrAlreadyRunning indicates another daemon owns the endpoint.
var ErrAlreadyRunning = errors.New("daemon already running")

// Endpoint returns the Unix socket path. GO_AWS_SOCKET overrides it for tests.
func Endpoint() (string, error) {
	if v := os.Getenv("GO_AWS_SOCKET"); v != "" {
		if err := os.MkdirAll(filepath.Dir(v), 0o700); err != nil {
			return "", err
		}
		return v, nil
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("go-aws-%d", os.Getuid()))
	path := filepath.Join(dir, "daemon.sock")
	// macOS caps Unix socket paths at 104 bytes, Linux at 108.
	if len(path) > 100 {
		dir = filepath.Join("/tmp", fmt.Sprintf("go-aws-%d", os.Getuid()))
		path = filepath.Join(dir, "daemon.sock")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

// Listen creates the Unix socket listener, replacing a stale socket from a
// crashed daemon. A live daemon yields ErrAlreadyRunning.
func Listen() (net.Listener, error) {
	path, err := Endpoint()
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		conn, derr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			return nil, ErrAlreadyRunning
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &cleanupListener{Listener: ln, path: path}, nil
}

// dialEndpoint connects to an existing daemon socket.
func dialEndpoint(timeout time.Duration) (net.Conn, error) {
	path, err := Endpoint()
	if err != nil {
		return nil, err
	}
	return net.DialTimeout("unix", path, timeout)
}

type cleanupListener struct {
	net.Listener
	path string
}

func (l *cleanupListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(l.path)
	return err
}
