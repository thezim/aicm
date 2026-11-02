package editor

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveConfigured(t *testing.T) {
	got, err := Resolve([]string{"code", "--wait", "{file}"}, "/tmp/my config.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"code", "--wait", "/tmp/my config.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveConfiguredRequiresPlaceholder(t *testing.T) {
	if _, err := Resolve([]string{"vim"}, "/tmp/config.yml"); err == nil {
		t.Fatal("expected an error for a configured command without {file}")
	}
}

func TestResolvePrefersVisual(t *testing.T) {
	t.Setenv("VISUAL", "code --wait")
	t.Setenv("EDITOR", "vim")
	got, err := Resolve(nil, "/tmp/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"code", "--wait", "/tmp/config.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveEditorFallback(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "'/path with space/editor' --flag")
	got, err := Resolve(nil, "/tmp/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/path with space/editor", "--flag", "/tmp/config.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveDefault(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	got, err := Resolve(nil, "/tmp/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[len(got)-1] != "/tmp/config.yml" {
		t.Fatalf("default command missing file: %v", got)
	}
	if strings.Contains(got[0], "{file}") {
		t.Fatalf("placeholder was not replaced: %v", got)
	}
}

func TestSplitCommand(t *testing.T) {
	got, err := splitCommand(`code --wait --user-data-dir="/tmp/a b" 'file name'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"code", "--wait", "--user-data-dir=/tmp/a b", "file name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, bad := range []string{`code "unterminated`, ""} {
		if _, err := splitCommand(bad); err == nil {
			t.Fatalf("expected an error for %q", bad)
		}
	}
}
