//go:build windows

package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// ErrAlreadyRunning indicates another daemon owns the endpoint.
var ErrAlreadyRunning = errors.New("daemon already running")

// Endpoint returns the current user's named pipe path.
func Endpoint() (string, error) {
	if v := os.Getenv("GO_AWS_PIPE"); v != "" {
		return v, nil
	}
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return `\\.\pipe\go-aws-` + sid, nil
}

func currentUserSID() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", fmt.Errorf("open process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("get token user: %w", err)
	}
	return user.User.Sid.String(), nil
}

func securityDescriptor() string {
	sid, err := currentUserSID()
	if err != nil {
		return ""
	}
	return "D:P(A;;GA;;;" + sid + ")"
}

// Listen creates a named pipe listener restricted to the current user.
func Listen() (net.Listener, error) {
	path, err := Endpoint()
	if err != nil {
		return nil, err
	}
	cfg := &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor(),
		MessageMode:        false,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	}
	ln, err := winio.ListenPipe(path, cfg)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return ln, nil
}

// dialEndpoint connects to an existing daemon named pipe.
func dialEndpoint(timeout time.Duration) (net.Conn, error) {
	path, err := Endpoint()
	if err != nil {
		return nil, err
	}
	return winio.DialPipe(path, &timeout)
}
