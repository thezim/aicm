package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsAndExplicitFalse(t *testing.T) {
	path := writeConfig(t, `
app:
  quit_after_rdp: false
accounts:
  "123456789012":
    colors:
      list_foreground: "#ffffff"
    rdp:
      full_screen: false
      clipboard: false
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.File.App.QuitAfterRDP {
		t.Fatal("quit_after_rdp=false was overwritten by defaults")
	}
	acct, ok := cfg.Account("123456789012")
	if !ok {
		t.Fatal("account missing")
	}
	if acct.RDP.Clipboard {
		t.Fatal("clipboard=false was overwritten")
	}
	if acct.RDP.FullScreen {
		t.Fatal("full_screen=false was overwritten")
	}
	if acct.RDP.Resolution != "1920x1080" {
		t.Fatalf("default resolution missing: %q", acct.RDP.Resolution)
	}
	if acct.Colors.ListForeground != "#ffffff" {
		t.Fatalf("color: %q", acct.Colors.ListForeground)
	}
	colors := cfg.EffectiveColors("123456789012")
	if colors.ListForeground != "#ffffff" {
		t.Fatalf("effective color: %q", colors.ListForeground)
	}
	if colors.ListBackground != DefaultColors().ListBackground {
		t.Fatalf("default color missing: %q", colors.ListBackground)
	}
	if len(cfg.File.AWS.States) != 0 {
		t.Fatalf("default states should be empty (all), got %v", cfg.File.AWS.States)
	}
}

func TestStatesEmptyMeansAll(t *testing.T) {
	path := writeConfig(t, `
aws:
  states: []
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.File.AWS.States) != 0 {
		t.Fatalf("empty states should remain empty, got %v", cfg.File.AWS.States)
	}
}

func TestStatesExplicitFilterIsPreserved(t *testing.T) {
	path := writeConfig(t, `
aws:
  states: [running, stopped]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(cfg.File.AWS.States, ","), "running,stopped"; got != want {
		t.Fatalf("states = %q, want %q", got, want)
	}
}

func TestEnsureAccountPreservesCommentsAndUnknownKeys(t *testing.T) {
	path := writeConfig(t, `# top comment
aws:
  # keep this state list
  states: [running, stopped]
accounts:
  "111111111111":
    # existing account comment
    rdp:
      username: bob
app:
  quit_after_rdp: true
custom_unknown:
  keep: me
`)
	res, err := EnsureAccount(context.Background(), path, "222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Added || res.WriteErr != nil {
		t.Fatalf("unexpected result: %+v", res)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"# top comment", "# keep this state list", "# existing account comment", "custom_unknown", `"222222222222":`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("written config does not parse: %v\n%s", err, s)
	}
	acct, ok := cfg.Account("222222222222")
	if !ok {
		t.Fatal("new account missing after reload")
	}
	if acct.RDP.RemotePort != 3389 || acct.RDP.Audio != "local" || !acct.RDP.Clipboard {
		t.Fatalf("new account not fully populated: %+v", acct.RDP)
	}
	existing, _ := cfg.Account("111111111111")
	if existing.RDP.Username != "bob" {
		t.Fatalf("existing account changed: %+v", existing)
	}
}

func TestEnsureAccountExistingIsNotRewritten(t *testing.T) {
	content := `accounts:
  "123456789012":
    rdp:
      username: alice
`
	path := writeConfig(t, content)
	res, err := EnsureAccount(context.Background(), path, "123456789012")
	if err != nil {
		t.Fatal(err)
	}
	if res.Added || res.WriteErr != nil {
		t.Fatalf("unexpected: %+v", res)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != content {
		t.Fatalf("existing config was rewritten:\n%s", raw)
	}
	if res.Account.RDP.Username != "alice" {
		t.Fatalf("existing value lost: %+v", res.Account)
	}
}

func TestEnsureAccountLeavesMalformedYAMLUntouched(t *testing.T) {
	content := "accounts: [this is not valid\n"
	path := writeConfig(t, content)
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := EnsureAccount(context.Background(), path, "123456789012"); err == nil {
		t.Fatal("expected ensure error")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != content {
		t.Fatalf("malformed config was modified:\n%s", raw)
	}
}

func TestEnsureAccountConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := EnsureAccount(context.Background(), path, "123456789012")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.File.Accounts) != 1 {
		t.Fatalf("expected exactly one account, got %d", len(cfg.File.Accounts))
	}
}

func TestAccountValidationWarnings(t *testing.T) {
	path := writeConfig(t, `
accounts:
  "123456789012":
    colors:
      list_foreground: "not-a-color"
    rdp:
      remote_port: 70000
      resolution: "huge"
      audio: "loud"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) < 3 {
		t.Fatalf("expected warnings, got %v", cfg.Warnings)
	}
	acct, _ := cfg.Account("123456789012")
	if acct.RDP.RemotePort != 3389 || acct.RDP.Resolution != "1920x1080" || acct.RDP.Audio != "local" {
		t.Fatalf("invalid values not normalized: %+v", acct.RDP)
	}
	if acct.Colors.ListForeground != "" {
		t.Fatalf("invalid account color should become an empty override: %q", acct.Colors.ListForeground)
	}
	if got := cfg.EffectiveColors("123456789012").ListForeground; got != DefaultColors().ListForeground {
		t.Fatalf("invalid color did not fall back to default: %q", got)
	}
}

func TestGlobalColorsAndAccountOverrides(t *testing.T) {
	path := writeConfig(t, `
app:
  colors:
    list_foreground: "#111111"
    status_background: "#222222"
    transient_background: "#666666"
    keys_foreground: "#777777"
    help_foreground: "#333333"
accounts:
  "123456789012":
    colors:
      list_foreground: "#444444"
      status_foreground: "#555555"
      transient_foreground: "#888888"
      keys_background: "#999999"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.EffectiveColors("123456789012")
	if got.ListForeground != "#444444" {
		t.Fatalf("account override = %q", got.ListForeground)
	}
	if got.StatusForeground != "#555555" {
		t.Fatalf("account status override = %q", got.StatusForeground)
	}
	if got.StatusBackground != "#222222" {
		t.Fatalf("global status background = %q", got.StatusBackground)
	}
	if got.TransientForeground != "#888888" {
		t.Fatalf("account transient override = %q", got.TransientForeground)
	}
	if got.TransientBackground != "#666666" {
		t.Fatalf("global transient background = %q", got.TransientBackground)
	}
	if got.KeysForeground != "#777777" {
		t.Fatalf("global keys foreground = %q", got.KeysForeground)
	}
	if got.KeysBackground != "#999999" {
		t.Fatalf("account keys background = %q", got.KeysBackground)
	}
	if got.HelpForeground != "#333333" {
		t.Fatalf("global help foreground = %q", got.HelpForeground)
	}
	if got.SelectedBackground != DefaultColors().SelectedBackground {
		t.Fatalf("built-in fallback = %q", got.SelectedBackground)
	}
	// Unknown accounts still get global colors.
	other := cfg.EffectiveColors("999999999999")
	if other.ListForeground != "#111111" || other.HelpForeground != "#333333" {
		t.Fatalf("global colors missing for unknown account: %+v", other)
	}
}

func TestInvalidGlobalColorFallsBack(t *testing.T) {
	path := writeConfig(t, `
app:
  colors:
    status_background: "not-a-color"
    help_background: "#abcdef"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) == 0 || !strings.Contains(strings.Join(cfg.Warnings, "\n"), "app.colors.status_background") {
		t.Fatalf("missing global color warning: %v", cfg.Warnings)
	}
	if cfg.File.App.Colors.StatusBackground != "" {
		t.Fatalf("invalid global color should be cleared: %q", cfg.File.App.Colors.StatusBackground)
	}
	got := cfg.EffectiveColors("123456789012")
	if got.StatusBackground != DefaultColors().StatusBackground {
		t.Fatalf("invalid global color did not fall back: %q", got.StatusBackground)
	}
	if got.HelpBackground != "#abcdef" {
		t.Fatalf("valid global color lost: %q", got.HelpBackground)
	}
}

func TestEditorValidation(t *testing.T) {
	path := writeConfig(t, `
app:
  editor: ["code", "--wait", "{file}"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(cfg.File.App.Editor, " "), "code --wait {file}"; got != want {
		t.Fatalf("editor = %q, want %q", got, want)
	}

	path = writeConfig(t, `
app:
  editor: ["vim"]
`)
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.File.App.Editor != nil {
		t.Fatalf("invalid editor was kept: %v", cfg.File.App.Editor)
	}
	if !strings.Contains(strings.Join(cfg.Warnings, "\n"), "app.editor") {
		t.Fatalf("missing editor warning: %v", cfg.Warnings)
	}
}

func TestEnsureAccountInheritsGlobalColors(t *testing.T) {
	path := writeConfig(t, `
app:
  colors:
    list_background: "#101010"
`)
	res, err := EnsureAccount(context.Background(), path, "123456789012")
	if err != nil {
		t.Fatal(err)
	}
	if res.Account.Colors.ListBackground != "" {
		t.Fatalf("new account should not pin global colors: %+v", res.Account.Colors)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.EffectiveColors("123456789012").ListBackground; got != "#101010" {
		t.Fatalf("new account did not inherit global color: %q", got)
	}
}

func TestEnsureFileCreatesDefaultConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yml")
	if err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("created config does not parse: %v\n%s", err, raw)
	}
	if cfg.File.App.Colors.ListForeground != DefaultColors().ListForeground {
		t.Fatalf("default app colors missing: %+v", cfg.File.App.Colors)
	}
	for _, want := range []string{
		"editor: []",
		"status_background:",
		"transient_foreground:",
		"transient_error_foreground:",
		"keys_foreground:",
		"keys_background:",
		"help_background:",
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("created config is missing %q:\n%s", want, raw)
		}
	}
	before := string(raw)
	if err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != before {
		t.Fatal("EnsureFile rewrote an existing config")
	}
}
