package rdp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go-aws/internal/display"
	"go-aws/internal/model"
)

const defaultResolution = "1920x1080"

var resolutionRE = regexp.MustCompile(`^([0-9]{3,5})x([0-9]{3,5})$`)

// Build creates an .rdp file for a loopback forwarded RDP port.
func Build(opts model.RDPOptions, localPort int) ([]byte, error) {
	if localPort <= 0 || localPort > 65535 {
		return nil, fmt.Errorf("invalid local port %d", localPort)
	}
	w, h := resolution(opts.Resolution)
	audio := audioMode(opts.Audio)
	var b bytes.Buffer
	line := func(s string) {
		b.WriteString(s)
		b.WriteString("\r\n")
	}
	line("full address:s:127.0.0.1:" + strconv.Itoa(localPort))
	if u := cleanField(opts.Username); u != "" {
		line("username:s:" + u)
	}
	if d := cleanField(opts.Domain); d != "" {
		line("domain:s:" + d)
	}
	line("prompt for credentials:i:1")
	if opts.FullScreen {
		line("screen mode id:i:2")
	} else {
		line("screen mode id:i:1")
	}
	line("desktopwidth:i:" + strconv.Itoa(w))
	line("desktopheight:i:" + strconv.Itoa(h))
	line("use multimon:i:" + yesNo(opts.MultiMonitor))
	line("redirectclipboard:i:" + yesNo(opts.Clipboard))
	line("redirectprinters:i:" + yesNo(opts.Printers))
	line("redirectsmartcards:i:" + yesNo(opts.Smartcards))
	if opts.Drives {
		line("drivestoredirect:s:*")
	}
	if opts.USBDevices {
		line("devicestoredirect:s:*")
	}
	if opts.Webcam {
		line("camerastoredirect:s:*")
	}
	line("audiomode:i:" + audio)
	line("audiocapturemode:i:" + yesNo(opts.Microphone))
	return b.Bytes(), nil
}

func yesNo(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func audioMode(audio string) string {
	switch strings.ToLower(strings.TrimSpace(audio)) {
	case "remote":
		return "1"
	case "none":
		return "2"
	default:
		return "0"
	}
}

func resolution(s string) (int, int) {
	m := resolutionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		m = resolutionRE.FindStringSubmatch(defaultResolution)
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	if w < 640 || h < 480 || w > 7680 || h > 4320 {
		w, h = 1920, 1080
	}
	return w, h
}

func cleanField(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\r' || r == '\n' || r == 0 || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// FileName returns a safe, unique .rdp filename.
func FileName(instanceName, instanceID, connectionID string, localPort int) string {
	name := display.Sanitize(instanceName)
	if name == "" {
		name = instanceID
	}
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			b.WriteRune('_')
		default:
			if r < 0x20 {
				continue
			}
			b.WriteRune(r)
		}
	}
	name = strings.Trim(b.String(), " ._")
	if name == "" {
		name = "instance"
	}
	name = truncateRunes(name, 60)
	id := connectionID
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("%s-%d-%s.rdp", name, localPort, id)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// Write writes the RDP file with restrictive permissions and CRLF content.
func Write(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rdp-*")
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
	if _, err := tmp.Write(content); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// DefaultOpenCommand returns the OS association opener.
func DefaultOpenCommand() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open", "{file}"}
	case "windows":
		return []string{"cmd", "/c", "start", "", "{file}"}
	default:
		return []string{"xdg-open", "{file}"}
	}
}

// ValidateOpener checks a configured opener argv template.
func ValidateOpener(argv []string) bool {
	if len(argv) == 0 {
		return true
	}
	hasFile := false
	for _, a := range argv {
		if strings.ContainsRune(a, 0) {
			return false
		}
		if strings.Contains(a, "{file}") {
			hasFile = true
		}
	}
	return hasFile
}

// Open launches the OS handler for the .rdp file without a shell. It waits
// briefly to catch immediate failures, but does not wait for the RDP client.
func Open(ctx context.Context, opener []string, file string) error {
	argv := opener
	if len(argv) == 0 {
		argv = DefaultOpenCommand()
	}
	if !ValidateOpener(argv) {
		return fmt.Errorf("invalid RDP opener configuration")
	}
	args := make([]string, 0, len(argv))
	for _, a := range argv {
		args = append(args, strings.ReplaceAll(a, "{file}", file))
	}
	cmd := exec.Command(args[0], args[1:]...)
	var stderr bytes.Buffer
	cmd.Stdout = &stderr
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg != "" {
				return fmt.Errorf("%w: %s", err, msg)
			}
			return err
		}
		return nil
	case <-timer.C:
		// The opener may be the RDP client itself and can legitimately stay
		// running. It is reaped by the goroutine.
		return nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}
}
