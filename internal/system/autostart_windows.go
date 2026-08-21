//go:build windows

// Windows implementation of AutoStart. We write to the per-user
// Run key (HKCU\Software\Microsoft\Windows\CurrentVersion\Run) so
// the app starts without needing admin elevation.

package system

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// runKeyPath is the per-user Run registry path. HKCU is preferred
// over HKLM because it does not require administrator rights and
// follows the user across machines in domain environments.
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// runValueName is the value name used for our Run entry. Keep it
// short to stay within registry conventions.
const runValueName = "APIDistribution"

type winAutoStart struct{}

// newPlatformAutoStart returns the Windows AutoStart implementation.
func newPlatformAutoStart() AutoStart { return winAutoStart{} }

// openRunKey opens HKCU\...\Run for read+write. The caller is
// responsible for closing the returned key.
func openRunKey(readOnly bool) (registry.Key, error) {
	access := uint32(registry.QUERY_VALUE | registry.SET_VALUE)
	if readOnly {
		access = registry.QUERY_VALUE
	}
	return registry.OpenKey(registry.CURRENT_USER, runKeyPath, access)
}

// quotedExePath returns the current executable path wrapped in
// double quotes so paths with spaces survive registry parsing.
func quotedExePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	return `"` + strings.ReplaceAll(exe, `"`, `\"`) + `"`, nil
}

// Enabled reads the Run value and reports whether it currently
// points at us. A missing value is treated as "not enabled".
func (winAutoStart) Enabled() (bool, error) {
	k, err := openRunKey(true)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()

	_, _, err = k.GetStringValue(runValueName)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query Run value: %w", err)
	}
	return true, nil
}

// Enable writes the current executable path into the Run key so the
// OS launches it at next user login.
func (winAutoStart) Enable(name string) error {
	if name == "" {
		return fmt.Errorf("auto-start name is required")
	}
	k, err := openRunKey(false)
	if err != nil {
		return err
	}
	defer k.Close()

	path, err := quotedExePath()
	if err != nil {
		return err
	}
	if err := k.SetStringValue(runValueName, path); err != nil {
		return fmt.Errorf("write Run value: %w", err)
	}
	return nil
}

// Disable deletes the value if present. A missing value is treated
// as success because the end state matches what the caller asked for.
func (winAutoStart) Disable() error {
	k, err := openRunKey(false)
	if err != nil {
		return err
	}
	defer k.Close()

	if err := k.DeleteValue(runValueName); err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return fmt.Errorf("delete Run value: %w", err)
	}
	return nil
}
