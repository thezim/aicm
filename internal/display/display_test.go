package display

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSanitizeStripsTerminalControl(t *testing.T) {
	in := "\x1b[31mweb\x1b[0m\x1b]0;title\x07\tprod\n 01  "
	got := Sanitize(in)
	if got != "webprod 01" && got != "web prod 01" {
		t.Fatalf("unexpected sanitized name %q", got)
	}
	if strings.ContainsAny(got, "\x1b\n\t") {
		t.Fatalf("control characters remain: %q", got)
	}
}

func TestNamesUnnamedFallbackAndDuplicates(t *testing.T) {
	now := time.Now()
	in := []Instance{
		{InstanceID: "i-3", Name: "web", LaunchTime: now.Add(3 * time.Second)},
		{InstanceID: "i-1", Name: "web", LaunchTime: now},
		{InstanceID: "i-2", Name: "web", LaunchTime: now.Add(time.Second)},
		{InstanceID: "i-4", Name: "", LaunchTime: now},
	}
	got := Names(in)
	want := map[string]string{
		"i-1": "web-01",
		"i-2": "web-02",
		"i-3": "web-03",
		"i-4": "i-4",
	}
	for _, n := range got {
		if want[n.InstanceID] != n.Display {
			t.Errorf("%s: got %q want %q", n.InstanceID, n.Display, want[n.InstanceID])
		}
	}
}

func TestNamesNaturalSuffixCollision(t *testing.T) {
	now := time.Now()
	in := []Instance{
		{InstanceID: "i-1", Name: "web", LaunchTime: now},
		{InstanceID: "i-2", Name: "web", LaunchTime: now.Add(time.Second)},
		{InstanceID: "i-3", Name: "web-01", LaunchTime: now.Add(2 * time.Second)},
	}
	got := Names(in)
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n.Display] {
			t.Fatalf("duplicate display name %q in %+v", n.Display, got)
		}
		seen[n.Display] = true
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 unique names, got %v", seen)
	}
}

func TestNamesManyDuplicates(t *testing.T) {
	in := make([]Instance, 100)
	for i := range in {
		in[i] = Instance{InstanceID: fmt.Sprintf("i-%03d", i), Name: "node", LaunchTime: time.Unix(int64(i), 0)}
	}
	got := Names(in)
	if len(got) != 100 {
		t.Fatalf("got %d", len(got))
	}
	byID := map[string]string{}
	for _, n := range got {
		byID[n.InstanceID] = n.Display
	}
	if byID["i-000"] != "node-01" || byID["i-099"] != "node-100" {
		t.Fatalf("unexpected numbering: first=%q last=%q", byID["i-000"], byID["i-099"])
	}
	seen := map[string]bool{}
	for _, n := range got {
		if seen[n.Display] {
			t.Fatalf("duplicate %q", n.Display)
		}
		seen[n.Display] = true
	}
}

func TestTruncateDisplayPreservesSuffix(t *testing.T) {
	n := Named{Base: "very-long-instance-name", Suffix: "-02", Display: "very-long-instance-name-02"}
	got := TruncateDisplay(n, 12)
	if !strings.HasSuffix(got, "-02") {
		t.Fatalf("suffix lost: %q", got)
	}
	if w := len([]rune(got)); w > 12 {
		t.Fatalf("too wide: %q", got)
	}
}
