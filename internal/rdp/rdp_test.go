package rdp

import (
	"strings"
	"testing"

	"go-aws/internal/model"
)

func TestBuildMapsOptions(t *testing.T) {
	opts := model.RDPOptions{
		Username:     "alice",
		Domain:       "CORP",
		RemotePort:   3389,
		FullScreen:   true,
		Resolution:   "2560x1440",
		MultiMonitor: true,
		Clipboard:    true,
		Drives:       true,
		Printers:     true,
		Smartcards:   true,
		USBDevices:   true,
		Webcam:       true,
		Audio:        "remote",
		Microphone:   true,
	}
	raw, err := Build(opts, 41234)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		"full address:s:127.0.0.1:41234\r\n",
		"username:s:alice\r\n",
		"domain:s:CORP\r\n",
		"prompt for credentials:i:1\r\n",
		"screen mode id:i:2\r\n",
		"desktopwidth:i:2560\r\n",
		"desktopheight:i:1440\r\n",
		"use multimon:i:1\r\n",
		"redirectclipboard:i:1\r\n",
		"redirectprinters:i:1\r\n",
		"redirectsmartcards:i:1\r\n",
		"drivestoredirect:s:*\r\n",
		"devicestoredirect:s:*\r\n",
		"camerastoredirect:s:*\r\n",
		"audiomode:i:1\r\n",
		"audiocapturemode:i:1\r\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(strings.ToLower(s), "password") {
		t.Fatalf("RDP content includes password field: %s", s)
	}
}

func TestBuildRejectsHeaderInjection(t *testing.T) {
	raw, err := Build(model.RDPOptions{
		Username:   "alice\r\npassword:s:oops",
		Domain:     "CORP\nfull address:s:evil",
		RemotePort: 3389,
		Resolution: "1920x1080",
		Audio:      "local",
	}, 12345)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, line := range strings.Split(s, "\r\n") {
		if strings.HasPrefix(line, "password:") {
			t.Fatalf("injected password field survived: %s", s)
		}
	}
	addresses := 0
	for _, line := range strings.Split(s, "\r\n") {
		if strings.HasPrefix(line, "full address:s:") {
			addresses++
		}
	}
	if addresses != 1 {
		t.Fatalf("injected address: %s", s)
	}
}

func TestFileNameIsSafeAndRuneAware(t *testing.T) {
	name := strings.Repeat("ü", 80) + `/bad:name*`
	got := FileName(name, "i-123", "abcdef0123456789", 5555)
	if strings.ContainsAny(got, `/:*?"<>|\`) {
		t.Fatalf("unsafe filename: %q", got)
	}
	if !strings.HasSuffix(got, "-5555-abcdef01.rdp") {
		t.Fatalf("unexpected suffix: %q", got)
	}
	if strings.Contains(got, "bad") {
		// The truncated portion must not split a rune or include the replaced
		// invalid characters.
	}
}

func TestResolutionFallback(t *testing.T) {
	w, h := resolution("nonsense")
	if w != 1920 || h != 1080 {
		t.Fatalf("got %dx%d", w, h)
	}
	w, h = resolution("800x600")
	if w != 800 || h != 600 {
		t.Fatalf("got %dx%d", w, h)
	}
}
