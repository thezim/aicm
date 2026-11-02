//go:build !windows

package platform

import (
	"errors"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// Tree supervises a process group so a tunnel and its descendants can be
// terminated together.
type Tree struct {
	cmd  *exec.Cmd
	pid  int
	dead bool
}

func NewTree() *Tree { return &Tree{} }

// Configure puts the child in its own process group.
func (t *Tree) Configure(cmd *exec.Cmd) {
	t.cmd = cmd
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// Attach records the started process. The process group id equals the pid
// because Setpgid is set in Configure.
func (t *Tree) Attach(cmd *exec.Cmd) error {
	t.cmd = cmd
	if cmd.Process != nil {
		t.pid = cmd.Process.Pid
	}
	return nil
}

// Kill sends SIGTERM to the process group, then SIGKILL after a grace period.
func (t *Tree) Kill() error {
	if t.dead || t.pid <= 0 {
		return nil
	}
	t.dead = true
	pgid := t.pid
	termErr := syscall.Kill(-pgid, syscall.SIGTERM)
	if termErr != nil && !errors.Is(termErr, syscall.ESRCH) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return termErr
	}
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) != nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	killErr := syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(killErr, syscall.ESRCH) {
		return nil
	}
	return killErr
}

func (t *Tree) Close() {}

// Detach configures a command to survive its parent (the TUI) in a new session.
func Detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// ProcessAlive reports whether a PID exists.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// CommandLine returns the process command line, used to avoid killing a
// recycled PID during crash recovery.
func CommandLine(pid int) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// TerminatePID terminates a stale process group recorded in crash state.
func TerminatePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}
