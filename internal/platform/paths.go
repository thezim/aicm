package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

func ensureDir(path string) (string, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o700)
	return path, nil
}

// CacheDir returns the per-user cache directory for go-aws.
func CacheDir() (string, error) {
	if v := os.Getenv("GO_AWS_CACHE_DIR"); v != "" {
		return ensureDir(v)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache directory: %w", err)
	}
	return ensureDir(filepath.Join(base, "go-aws"))
}

// RDPDir returns the directory used for generated .rdp files.
func RDPDir() (string, error) {
	base, err := CacheDir()
	if err != nil {
		return "", err
	}
	return ensureDir(filepath.Join(base, "rdp"))
}

// LogPath returns the daemon log path.
func LogPath() (string, error) {
	base, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "daemon.log"), nil
}

// StatePath returns the crash-recovery state path.
func StatePath() (string, error) {
	base, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "daemon-state.json"), nil
}

// LockPath returns the single-instance daemon lock path.
func LockPath() (string, error) {
	base, err := CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "daemon.lock"), nil
}
