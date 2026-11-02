//go:build !windows

package platform

import (
	"os"
	"syscall"
)

// Signals returns the shutdown signals for the platform.
func Signals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
