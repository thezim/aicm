//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Tree supervises a child with a Windows Job Object so the tunnel and its
// descendants (for example session-manager-plugin) are terminated together.
type Tree struct {
	cmd    *exec.Cmd
	job    windows.Handle
	pid    int
	dead   bool
	hasJob bool
}

func NewTree() *Tree { return &Tree{} }

// Configure hides the console window and gives the child its own process group.
func (t *Tree) Configure(cmd *exec.Cmd) {
	t.cmd = cmd
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

// Attach creates a kill-on-close job and assigns the started process to it.
// If job assignment fails (for example when the daemon itself runs inside a
// restrictive job), fall back to taskkill for cleanup.
func (t *Tree) Attach(cmd *exec.Cmd) error {
	t.cmd = cmd
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	t.pid = cmd.Process.Pid
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(t.pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return nil
	}
	t.job = job
	t.hasJob = true
	return nil
}

func (t *Tree) Kill() error {
	if t.dead {
		return nil
	}
	t.dead = true
	if t.hasJob {
		err := windows.TerminateJobObject(t.job, 1)
		if err == nil || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil
		}
	}
	if t.pid > 0 {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(t.pid)).Run()
	}
	return nil
}

func (t *Tree) Close() {
	if t.hasJob {
		_ = windows.CloseHandle(t.job)
		t.hasJob = false
	}
}

// Detach configures a command to run without a console and survive the TUI.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

// ProcessAlive reports whether a PID is still running.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// CommandLine returns the process command line for crash-recovery checks.
func CommandLine(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid")
	}
	script := fmt.Sprintf("(Get-CimInstance Win32_Process -Filter \"ProcessId=%d\").CommandLine", pid)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// TerminatePID forcefully terminates a stale process tree.
func TerminatePID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
