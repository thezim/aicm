//go:build windows

package platform

import "os"

// Signals returns the shutdown signals for the platform.
func Signals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
