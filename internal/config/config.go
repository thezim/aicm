package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/thezim/aicm/internal/editor"
	"github.com/thezim/aicm/internal/model"
	"gopkg.in/yaml.v3"
)

const (
	DefaultRegionPlaceholder = ""
	DefaultConnectTimeout    = "30s"
	DefaultIdleExit          = "5s"
)

var (
	resolutionRE = regexp.MustCompile(`^([0-9]{3,5})x([0-9]{3,5})$`)
	ansiRE       = regexp.MustCompile(`^(?:#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})|[0-9]{1,3}|default)$`)
)

type AWSSection struct {
	States []string `yaml:"states"`
}

type AppSection struct {
	QuitAfterRDP bool         `yaml:"quit_after_rdp"`
	RDPOpener    []string     `yaml:"rdp_opener"`
	Editor       []string     `yaml:"editor"`
	Colors       model.Colors `yaml:"colors"`
}

type DaemonSection struct {
	ConnectTimeout string `yaml:"connect_timeout"`
	IdleExit       string `yaml:"idle_exit"`
}

type Account struct {
	Colors model.Colors     `yaml:"colors,omitempty"`
	RDP    model.RDPOptions `yaml:"rdp"`
}

type File struct {
	AWS      AWSSection         `yaml:"aws"`
	Accounts map[string]Account `yaml:"accounts"`
	App      AppSection         `yaml:"app"`
	Daemon   DaemonSection      `yaml:"daemon"`
}

// Config is the loaded configuration plus the raw YAML tree used when adding
// a new account without destroying comments or unknown fields.
type Config struct {
	Path     string
	File     File
	Root     *yaml.Node
	Exists   bool
	Warnings []string
}

// DefaultColors returns the built-in TUI theme.
func DefaultColors() model.Colors {
	return model.Colors{
		ListForeground:           "#cdd6f4",
		ListBackground:           "#1e1e2e",
		SelectedForeground:       "#1e1e2e",
		SelectedBackground:       "#89b4fa",
		HeaderForeground:         "#89b4fa",
		HeaderBackground:         "#1e1e2e",
		StatusForeground:         "#89b4fa",
		StatusBackground:         "#1e1e2e",
		TransientForeground:      "#89b4fa",
		TransientBackground:      "#1e1e2e",
		TransientErrorForeground: "#f38ba8",
		KeysForeground:           "#89b4fa",
		KeysBackground:           "#1e1e2e",
		HelpForeground:           "#cdd6f4",
		HelpBackground:           "#1e1e2e",
	}
}

// DefaultRDP returns the built-in RDP options.
func DefaultRDP() model.RDPOptions {
	return model.RDPOptions{
		RemotePort: 3389,
		FullScreen: false,
		Resolution: "1920x1080",
		Clipboard:  true,
		Audio:      "local",
	}
}

func DefaultAccount() Account {
	return Account{RDP: DefaultRDP()}
}

func DefaultAWS() AWSSection {
	return AWSSection{}
}

func DefaultApp() AppSection {
	return AppSection{QuitAfterRDP: true, Colors: DefaultColors()}
}

func DefaultDaemon() DaemonSection {
	return DaemonSection{ConnectTimeout: DefaultConnectTimeout, IdleExit: DefaultIdleExit}
}

func DefaultFile() File {
	return File{
		AWS:      DefaultAWS(),
		Accounts: map[string]Account{},
		App:      DefaultApp(),
		Daemon:   DefaultDaemon(),
	}
}

func (a *AWSSection) UnmarshalYAML(value *yaml.Node) error {
	type plain AWSSection
	tmp := plain(DefaultAWS())
	if err := value.Decode(&tmp); err != nil {
		return err
	}
	*a = AWSSection(tmp)
	return nil
}

func (a *AppSection) UnmarshalYAML(value *yaml.Node) error {
	type plain AppSection
	tmp := plain(DefaultApp())
	if err := value.Decode(&tmp); err != nil {
		return err
	}
	*a = AppSection(tmp)
	return nil
}

func (d *DaemonSection) UnmarshalYAML(value *yaml.Node) error {
	type plain DaemonSection
	tmp := plain(DefaultDaemon())
	if err := value.Decode(&tmp); err != nil {
		return err
	}
	*d = DaemonSection(tmp)
	return nil
}

func (a *Account) UnmarshalYAML(value *yaml.Node) error {
	type plain Account
	tmp := plain(DefaultAccount())
	if err := value.Decode(&tmp); err != nil {
		return err
	}
	*a = Account(tmp)
	return nil
}

// DefaultPath returns ~/.config/aicm/config.yml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "aicm", "config.yml"), nil
}

// ExpandPath expands a leading ~.
func ExpandPath(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func Load(path string) (*Config, error) {
	path = ExpandPath(path)
	cfg := &Config{Path: path, File: DefaultFile()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg.Root = newDefaultDocument()
		cfg.Warnings = cfg.validateAndNormalize()
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg.Exists = true
	doc, err := parseDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg.File); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.Root = doc
	cfg.Warnings = cfg.validateAndNormalize()
	return cfg, nil
}

// Account returns an effective account entry and whether it exists.
func (c *Config) Account(id string) (Account, bool) {
	if c == nil || c.File.Accounts == nil {
		return DefaultAccount(), false
	}
	a, ok := c.File.Accounts[id]
	if !ok {
		return DefaultAccount(), false
	}
	return a, true
}

func (c *Config) AccountOrDefault(id string) Account {
	a, ok := c.Account(id)
	if !ok {
		return DefaultAccount()
	}
	return a
}

// EffectiveColors merges built-in defaults, global app.colors, and per-account
// overrides, in that order. Empty values inherit from the previous level.
func (c *Config) EffectiveColors(id string) model.Colors {
	out := DefaultColors()
	if c == nil {
		return out
	}
	overlayColors(&out, c.File.App.Colors)
	if a, ok := c.Account(id); ok {
		overlayColors(&out, a.Colors)
	}
	return out
}

func overlayColors(dst *model.Colors, src model.Colors) {
	overlayColor(&dst.ListForeground, src.ListForeground)
	overlayColor(&dst.ListBackground, src.ListBackground)
	overlayColor(&dst.SelectedForeground, src.SelectedForeground)
	overlayColor(&dst.SelectedBackground, src.SelectedBackground)
	overlayColor(&dst.HeaderForeground, src.HeaderForeground)
	overlayColor(&dst.HeaderBackground, src.HeaderBackground)
	overlayColor(&dst.StatusForeground, src.StatusForeground)
	overlayColor(&dst.StatusBackground, src.StatusBackground)
	overlayColor(&dst.TransientForeground, src.TransientForeground)
	overlayColor(&dst.TransientBackground, src.TransientBackground)
	overlayColor(&dst.TransientErrorForeground, src.TransientErrorForeground)
	overlayColor(&dst.KeysForeground, src.KeysForeground)
	overlayColor(&dst.KeysBackground, src.KeysBackground)
	overlayColor(&dst.HelpForeground, src.HelpForeground)
	overlayColor(&dst.HelpBackground, src.HelpBackground)
}

func overlayColor(dst *string, src string) {
	if src != "" {
		*dst = src
	}
}

func (c *Config) SetAccount(id string, a Account) {
	if c.File.Accounts == nil {
		c.File.Accounts = make(map[string]Account)
	}
	c.File.Accounts[id] = a
}

func (c *Config) ConnectTimeout() time.Duration {
	return durationOrDefault(c.File.Daemon.ConnectTimeout, 30*time.Second)
}

func (c *Config) IdleExit() time.Duration {
	return durationOrDefault(c.File.Daemon.IdleExit, 5*time.Second)
}

func durationOrDefault(s string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// EnsureResult describes the outcome of EnsureAccount.
type EnsureResult struct {
	Account  Account
	Added    bool
	Warnings []string
	WriteErr error
}

// EnsureAccount atomically adds an account entry if it is missing. Existing
// entries are never rewritten. A WriteErr is returned separately so the caller
// can continue with in-memory defaults.
func EnsureAccount(ctx context.Context, path, accountID string) (EnsureResult, error) {
	path = ExpandPath(path)
	target, err := resolveSymlink(path)
	if err != nil {
		return EnsureResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return EnsureResult{}, fmt.Errorf("create config directory: %w", err)
	}
	lock := flock.New(target + ".lock")
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("lock config: %w", err)
	}
	if !locked {
		return EnsureResult{}, fmt.Errorf("timed out locking config %s", target)
	}
	defer func() {
		_ = lock.Unlock()
		_ = lock.Close()
	}()

	data, err := os.ReadFile(target)
	exists := true
	if errors.Is(err, os.ErrNotExist) {
		exists = false
		data = nil
	} else if err != nil {
		return EnsureResult{}, fmt.Errorf("read config %s: %w", target, err)
	}

	cfg := DefaultFile()
	if exists {
		doc, err := parseDocument(data)
		if err != nil {
			return EnsureResult{}, fmt.Errorf("parse config %s: %w", target, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return EnsureResult{}, fmt.Errorf("parse config %s: %w", target, err)
		}
		_ = doc
	}
	if a, ok := cfg.Accounts[accountID]; ok {
		normalized, warnings := normalizeAccount(accountID, a)
		return EnsureResult{Account: normalized, Warnings: warnings}, nil
	}

	acct := DefaultAccount()
	doc := newDefaultDocument()
	if exists {
		doc, err = parseDocument(data)
		if err != nil {
			return EnsureResult{}, fmt.Errorf("parse config %s: %w", target, err)
		}
	}
	if err := insertAccount(doc, accountID, acct); err != nil {
		return EnsureResult{}, err
	}
	out, err := encodeDocument(doc)
	if err != nil {
		return EnsureResult{}, fmt.Errorf("encode config: %w", err)
	}
	res := EnsureResult{Account: acct, Added: true, Warnings: validateAccount(accountID, acct)}
	if err := writeAtomic(target, out); err != nil {
		res.WriteErr = err
		return res, nil
	}
	return res, nil
}

// EnsureFile creates a default config file when it does not exist. Existing
// files are never modified.
func EnsureFile(path string) error {
	path = ExpandPath(path)
	target, err := resolveSymlink(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	doc := newDefaultDocument()
	data, err := encodeDocument(doc)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeAtomic(target, data)
}

func (c *Config) validateAndNormalize() []string {
	var warnings []string
	var states []string
	seen := map[string]bool{}
	for _, s := range c.File.AWS.States {
		s = strings.ToLower(strings.TrimSpace(s))
		if !validState(s) {
			warnings = append(warnings, fmt.Sprintf("aws.states: ignoring unknown state %q", s))
			continue
		}
		if !seen[s] {
			states = append(states, s)
			seen[s] = true
		}
	}
	c.File.AWS.States = states

	if len(c.File.App.RDPOpener) > 0 {
		opener, ok := normalizeOpener(c.File.App.RDPOpener)
		if !ok {
			warnings = append(warnings, "app.rdp_opener: invalid opener; using OS default")
			c.File.App.RDPOpener = nil
		} else {
			c.File.App.RDPOpener = opener
		}
	}
	if len(c.File.App.Editor) > 0 {
		if !editor.Validate(c.File.App.Editor) {
			warnings = append(warnings, "app.editor: invalid editor; using $VISUAL/$EDITOR or the OS default")
			c.File.App.Editor = nil
		}
	}
	normalizeColors("app.colors", &c.File.App.Colors, &warnings)
	if _, err := time.ParseDuration(strings.TrimSpace(c.File.Daemon.ConnectTimeout)); err != nil || c.File.Daemon.ConnectTimeout == "" {
		warnings = append(warnings, fmt.Sprintf("daemon.connect_timeout: invalid %q; using %s", c.File.Daemon.ConnectTimeout, DefaultConnectTimeout))
		c.File.Daemon.ConnectTimeout = DefaultConnectTimeout
	}
	if _, err := time.ParseDuration(strings.TrimSpace(c.File.Daemon.IdleExit)); err != nil || c.File.Daemon.IdleExit == "" {
		warnings = append(warnings, fmt.Sprintf("daemon.idle_exit: invalid %q; using %s", c.File.Daemon.IdleExit, DefaultIdleExit))
		c.File.Daemon.IdleExit = DefaultIdleExit
	}

	ids := make([]string, 0, len(c.File.Accounts))
	for id := range c.File.Accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a, w := normalizeAccount(id, c.File.Accounts[id])
		c.File.Accounts[id] = a
		warnings = append(warnings, w...)
	}
	return warnings
}

func normalizeAccount(id string, a Account) (Account, []string) {
	def := DefaultAccount()
	warnings := validateAccount(id, a)
	normalizeColors(fmt.Sprintf("accounts.%s.colors", id), &a.Colors, &warnings)

	if a.RDP.RemotePort == 0 {
		a.RDP.RemotePort = def.RDP.RemotePort
	}
	if a.RDP.Resolution == "" {
		a.RDP.Resolution = def.RDP.Resolution
	}
	if a.RDP.Audio == "" {
		a.RDP.Audio = def.RDP.Audio
	}
	if !validPort(a.RDP.RemotePort) {
		a.RDP.RemotePort = def.RDP.RemotePort
	}
	if _, _, ok := parseResolution(a.RDP.Resolution); !ok {
		a.RDP.Resolution = def.RDP.Resolution
	}
	if !validAudio(a.RDP.Audio) {
		a.RDP.Audio = def.RDP.Audio
	}
	a.RDP.Username = sanitizeRDPField(a.RDP.Username)
	a.RDP.Domain = sanitizeRDPField(a.RDP.Domain)
	return a, warnings
}

func validateAccount(id string, a Account) []string {
	var w []string
	prefix := fmt.Sprintf("accounts.%s", id)
	if a.RDP.RemotePort != 0 && !validPort(a.RDP.RemotePort) {
		w = append(w, fmt.Sprintf("%s.rdp.remote_port: invalid port %d", prefix, a.RDP.RemotePort))
	}
	if a.RDP.Resolution != "" {
		if _, _, ok := parseResolution(a.RDP.Resolution); !ok {
			w = append(w, fmt.Sprintf("%s.rdp.resolution: invalid value %q", prefix, a.RDP.Resolution))
		}
	}
	if a.RDP.Audio != "" && !validAudio(a.RDP.Audio) {
		w = append(w, fmt.Sprintf("%s.rdp.audio: invalid value %q (want local, remote, or none)", prefix, a.RDP.Audio))
	}
	if containsControl(a.RDP.Username) || containsControl(a.RDP.Domain) {
		w = append(w, fmt.Sprintf("%s.rdp.username/domain: control characters removed", prefix))
	}
	return w
}

func normalizeColors(prefix string, c *model.Colors, warnings *[]string) {
	fields := []struct {
		name string
		ptr  *string
	}{
		{"list_foreground", &c.ListForeground},
		{"list_background", &c.ListBackground},
		{"selected_foreground", &c.SelectedForeground},
		{"selected_background", &c.SelectedBackground},
		{"header_foreground", &c.HeaderForeground},
		{"header_background", &c.HeaderBackground},
		{"status_foreground", &c.StatusForeground},
		{"status_background", &c.StatusBackground},
		{"transient_foreground", &c.TransientForeground},
		{"transient_background", &c.TransientBackground},
		{"transient_error_foreground", &c.TransientErrorForeground},
		{"keys_foreground", &c.KeysForeground},
		{"keys_background", &c.KeysBackground},
		{"help_foreground", &c.HelpForeground},
		{"help_background", &c.HelpBackground},
	}
	for _, f := range fields {
		if *f.ptr == "" {
			continue
		}
		if !validColor(*f.ptr) {
			*warnings = append(*warnings, fmt.Sprintf("%s.%s: invalid color %q", prefix, f.name, *f.ptr))
			*f.ptr = ""
		}
	}
}

func validState(s string) bool {
	switch s {
	case "pending", "running", "shutting-down", "terminated", "stopping", "stopped":
		return true
	}
	return false
}

func validColor(s string) bool {
	if !ansiRE.MatchString(s) {
		return false
	}
	if strings.HasPrefix(s, "#") {
		return true
	}
	if s == "default" {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 0 && n <= 255
}

func validPort(p int) bool {
	return p > 0 && p <= 65535
}

func validAudio(s string) bool {
	switch strings.ToLower(s) {
	case "local", "remote", "none":
		return true
	}
	return false
}

func parseResolution(s string) (int, int, bool) {
	m := resolutionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	w, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	if w < 640 || h < 480 || w > 7680 || h > 4320 {
		return 0, 0, false
	}
	return w, h, true
}

// Resolution parses a resolution string, returning a safe fallback on error.
func Resolution(s string) (int, int) {
	w, h, ok := parseResolution(s)
	if !ok {
		w, h, _ = parseResolution(DefaultRDP().Resolution)
	}
	return w, h
}

func sanitizeRDPField(s string) string {
	s = strings.TrimSpace(s)
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

func containsControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func normalizeOpener(in []string) ([]string, bool) {
	out := make([]string, 0, len(in))
	placeholder := false
	for _, s := range in {
		if strings.ContainsRune(s, 0) {
			return nil, false
		}
		if strings.Contains(s, "{file}") {
			placeholder = true
		}
		out = append(out, s)
	}
	if len(out) == 0 || !placeholder {
		return nil, false
	}
	return out, true
}

func resolveSymlink(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return abs, nil
		}
		return "", err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return abs, nil
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve config symlink: %w", err)
	}
	return target, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config.yml.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

func parseDocument(data []byte) (*yaml.Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return newDefaultDocument(), nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		return newDefaultDocument(), nil
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 || isNullNode(doc.Content[0]) {
			return newDefaultDocument(), nil
		}
		if doc.Content[0].Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config root must be a YAML mapping")
		}
		return &doc, nil
	}
	if isNullNode(&doc) {
		return newDefaultDocument(), nil
	}
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config root must be a YAML mapping")
	}
	root := &doc
	wrapped := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	return wrapped, nil
}

func isNullNode(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || n.Value == "null" || n.Value == "~"))
}

func newDefaultDocument() *yaml.Node {
	return newDocumentWithAccount("", Account{}, false)
}

func newDocumentWithAccount(id string, acct Account, include bool) *yaml.Node {
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, scalar("aws"), encodeNode(DefaultAWS()))
	accounts := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if include {
		accounts.Content = append(accounts.Content, accountKey(id), encodeNode(acct))
	}
	root.Content = append(root.Content, scalar("accounts"), accounts)
	root.Content = append(root.Content, scalar("app"), encodeNode(DefaultApp()))
	root.Content = append(root.Content, scalar("daemon"), encodeNode(DefaultDaemon()))
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
}

func encodeNode(v any) *yaml.Node {
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return scalar(fmt.Sprint(v))
	}
	return n
}

func scalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func accountKey(id string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id, Style: yaml.DoubleQuotedStyle}
}

func insertAccount(doc *yaml.Node, id string, acct Account) error {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("invalid YAML document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("config root must be a YAML mapping")
	}
	var accounts *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "accounts" {
			if isNullNode(root.Content[i+1]) {
				root.Content[i+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			accounts = root.Content[i+1]
			break
		}
	}
	if accounts == nil {
		accounts = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, scalar("accounts"), accounts)
	}
	if accounts.Kind != yaml.MappingNode {
		return fmt.Errorf("accounts must be a YAML mapping")
	}
	for i := 0; i+1 < len(accounts.Content); i += 2 {
		if accounts.Content[i].Value == id {
			return nil
		}
	}
	val := encodeNode(acct)
	val.HeadComment = "Added automatically on first use."
	accounts.Content = append(accounts.Content, accountKey(id), val)
	return nil
}

func encodeDocument(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		_ = enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
