package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thezim/aicm/internal/platform"
)

type stateFile struct {
	Version int          `json:"version"`
	Entries []stateEntry `json:"entries"`
}

type stateEntry struct {
	PID        int    `json:"pid"`
	LocalPort  int    `json:"local_port"`
	RDPPath    string `json:"rdp_path,omitempty"`
	InstanceID string `json:"instance_id"`
	AccountID  string `json:"account_id,omitempty"`
	Command    string `json:"command,omitempty"`
}

// CleanupStale terminates tunnel process groups left behind by a daemon that
// was killed, and removes their RDP files. PIDs are only killed when the
// command line still matches an SSM session for the recorded instance.
func CleanupStale() {
	path, err := platform.StatePath()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(path)
		}
		return
	}
	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		_ = os.Remove(path)
		return
	}
	for _, e := range sf.Entries {
		if e.PID > 0 && platform.ProcessAlive(e.PID) {
			cmdline, err := platform.CommandLine(e.PID)
			if err == nil && looksLikeTunnel(cmdline, e) {
				_ = platform.TerminatePID(e.PID)
			}
		}
		if e.RDPPath != "" {
			_ = os.Remove(e.RDPPath)
		}
	}
	_ = os.Remove(path)
}

func looksLikeTunnel(cmdline string, e stateEntry) bool {
	lower := strings.ToLower(cmdline)
	if !strings.Contains(lower, "start-session") && !strings.Contains(lower, "session-manager-plugin") {
		return false
	}
	if e.InstanceID != "" && !strings.Contains(cmdline, e.InstanceID) {
		return false
	}
	if e.LocalPort > 0 && !strings.Contains(cmdline, fmt.Sprintf("%d", e.LocalPort)) {
		return false
	}
	return true
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
