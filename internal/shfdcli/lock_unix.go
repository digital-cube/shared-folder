//go:build unix

package shfdcli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDataRoot keeps a second shfd serve off the same data-root, so each
// folder has one writer across processes too.
func lockDataRoot(root string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(root, "serve.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("data-root %s is already served by another shfd: %w", root, err)
	}
	return func() { f.Close() }, nil
}
