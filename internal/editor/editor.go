// Package editor resolves the command used to edit the go-aws config file.
package editor

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"unicode"
)

const filePlaceholder = "{file}"

// Validate checks a configured editor argv template. Configured templates must
// contain the {file} placeholder; environment and default commands may omit it
// and get the path appended.
func Validate(argv []string) bool {
	if len(argv) == 0 {
		return true
	}
	if strings.TrimSpace(argv[0]) == "" {
		return false
	}
	hasFile := false
	for _, arg := range argv {
		if strings.ContainsRune(arg, 0) {
			return false
		}
		if strings.Contains(arg, filePlaceholder) {
			hasFile = true
		}
	}
	return hasFile
}

// DefaultCommand returns the OS file-association editor for the platform.
func DefaultCommand() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"open", "-W", "-t", filePlaceholder}
	case "windows":
		return []string{"notepad.exe", filePlaceholder}
	default:
		return []string{"xdg-open", filePlaceholder}
	}
}

// Resolve returns the argv used to open file. Configured commands must contain
// {file}. If no command is configured, $VISUAL, $EDITOR, and the OS default are
// tried in that order; those commands get file appended when needed.
func Resolve(configured []string, file string) ([]string, error) {
	argv := configured
	needsAppend := false
	switch {
	case len(argv) > 0:
		if !Validate(argv) {
			return nil, fmt.Errorf("app.editor must be a non-empty argv list containing %s", filePlaceholder)
		}
	case strings.TrimSpace(os.Getenv("VISUAL")) != "":
		var err error
		argv, err = splitCommand(os.Getenv("VISUAL"))
		if err != nil {
			return nil, fmt.Errorf("parse VISUAL: %w", err)
		}
		needsAppend = true
	case strings.TrimSpace(os.Getenv("EDITOR")) != "":
		var err error
		argv, err = splitCommand(os.Getenv("EDITOR"))
		if err != nil {
			return nil, fmt.Errorf("parse EDITOR: %w", err)
		}
		needsAppend = true
	default:
		argv = DefaultCommand()
		needsAppend = true
	}
	if strings.TrimSpace(argv[0]) == "" {
		return nil, fmt.Errorf("editor command is empty")
	}
	if needsAppend && !containsPlaceholder(argv) {
		argv = append(append([]string(nil), argv...), filePlaceholder)
	}
	out := make([]string, len(argv))
	for i, arg := range argv {
		out[i] = strings.ReplaceAll(arg, filePlaceholder, file)
	}
	return out, nil
}

func containsPlaceholder(argv []string) bool {
	for _, arg := range argv {
		if strings.Contains(arg, filePlaceholder) {
			return true
		}
	}
	return false
}

// splitCommand splits a shell-like command string without invoking a shell.
// Single and double quotes and backslash escapes are supported.
func splitCommand(s string) ([]string, error) {
	var (
		args     []string
		current  strings.Builder
		quote    rune
		escaped  bool
		hasToken bool
	)
	flush := func() {
		if hasToken {
			args = append(args, current.String())
			current.Reset()
			hasToken = false
		}
	}
	for _, r := range s {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
			hasToken = true
		case r == '\\' && quote != '\'':
			escaped = true
			hasToken = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			hasToken = true
		case r == '\'' || r == '"':
			quote = r
			hasToken = true
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
			hasToken = true
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return args, nil
}
