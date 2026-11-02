package display

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// Instance is the minimum input needed to assign display names.
type Instance struct {
	InstanceID string
	Name       string
	LaunchTime time.Time
}

// Named is an instance with its presentation name.
type Named struct {
	InstanceID string
	Name       string
	Base       string
	Suffix     string
	Display    string
	LaunchTime time.Time
}

// Sanitize removes terminal escape sequences and control characters, collapses
// whitespace, and trims the result. EC2 tags are user-controlled input.
func Sanitize(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == 0x1b {
			if i+1 < len(runes) {
				switch runes[i+1] {
				case '[':
					i += 2
					for i < len(runes) {
						c := runes[i]
						if c >= 0x40 && c <= 0x7e {
							break
						}
						i++
					}
				case ']':
					i += 2
					for i < len(runes) {
						if runes[i] == 0x07 {
							break
						}
						if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '\\' {
							i++
							break
						}
						i++
					}
				default:
					i++
				}
			}
			continue
		}
		if r == '\t' || r == '\r' || r == '\n' {
			b.WriteRune(' ')
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Names assigns displayed names. Duplicate Name tags get -01, -02, ... suffixes,
// numbered by launch time with instance ID as the tie-breaker. Missing names
// fall back to the instance ID. A final pass guarantees global uniqueness.
func Names(in []Instance) []Named {
	items := make([]Named, len(in))
	for i, inst := range in {
		base := Sanitize(inst.Name)
		if base == "" {
			base = inst.InstanceID
		}
		items[i] = Named{
			InstanceID: inst.InstanceID,
			Name:       inst.Name,
			Base:       base,
			LaunchTime: inst.LaunchTime,
			Display:    base,
		}
	}

	groups := map[string][]int{}
	for i := range items {
		groups[items[i].Base] = append(groups[items[i].Base], i)
	}
	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		sort.SliceStable(idxs, func(a, b int) bool {
			return less(items[idxs[a]], items[idxs[b]])
		})
		for j, idx := range idxs {
			suffix := fmt.Sprintf("-%02d", j+1)
			items[idx].Suffix = suffix
			items[idx].Display = items[idx].Base + suffix
		}
	}

	// Resolve collisions between generated names and natural names (for
	// example an actual instance named "web-01" alongside duplicated "web").
	for pass := 0; pass < 100; pass++ {
		collisions := map[string][]int{}
		for i := range items {
			collisions[items[i].Display] = append(collisions[items[i].Display], i)
		}
		changed := false
		for _, idxs := range collisions {
			if len(idxs) < 2 {
				continue
			}
			changed = true
			sort.SliceStable(idxs, func(a, b int) bool {
				return less(items[idxs[a]], items[idxs[b]])
			})
			for j, idx := range idxs {
				suffix := fmt.Sprintf("-%02d", j+1)
				items[idx].Suffix += suffix
				items[idx].Display += suffix
			}
		}
		if !changed {
			break
		}
		if pass == 99 {
			// Pathological adversarial names: keep the display unique without
			// losing the human-readable base.
			for i := range items {
				id := items[i].InstanceID
				if len(id) > 8 {
					id = id[len(id)-8:]
				}
				items[i].Suffix += "-" + id
				items[i].Display += "-" + id
			}
		}
	}

	out := make([]Named, len(items))
	for i := range items {
		out[i] = items[i]
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := strings.ToLower(out[i].Display), strings.ToLower(out[j].Display)
		if di != dj {
			return di < dj
		}
		if out[i].Display != out[j].Display {
			return out[i].Display < out[j].Display
		}
		return out[i].InstanceID < out[j].InstanceID
	})
	return out
}

func less(a, b Named) bool {
	if !a.LaunchTime.Equal(b.LaunchTime) {
		return a.LaunchTime.Before(b.LaunchTime)
	}
	return a.InstanceID < b.InstanceID
}

// TruncateDisplay truncates a name while preserving its duplicate/collision
// suffix.
func TruncateDisplay(n Named, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(n.Display) <= width {
		return n.Display
	}
	if n.Suffix == "" {
		return Truncate(n.Display, width)
	}
	suffixWidth := runewidth.StringWidth(n.Suffix)
	if suffixWidth >= width {
		return Truncate(n.Display, width)
	}
	return Truncate(n.Base, width-suffixWidth) + n.Suffix
}

// Truncate returns a rune-width-aware string with a trailing ellipsis.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	used := 0
	limit := width - 1
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if used+w > limit {
			break
		}
		b.WriteRune(r)
		used += w
	}
	b.WriteRune('…')
	return b.String()
}

// Pad truncates or right-pads a string to a visible width.
func Pad(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = Truncate(s, width)
	w := runewidth.StringWidth(s)
	if w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}
