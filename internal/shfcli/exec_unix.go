//go:build unix

package shfcli

import (
	"os"
	"syscall"
)

// execSelf replaces the process with exe (shf daemon after an update):
// the same PID, so systemd and launchd keep tracking it.
func execSelf(exe string, argv []string) error {
	return syscall.Exec(exe, argv, os.Environ())
}
