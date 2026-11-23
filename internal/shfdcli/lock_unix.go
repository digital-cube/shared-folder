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

// checkPrivateDir makes sure dir is a directory of this user that nobody
// else may enter, so a socket in it cannot be planted by another user.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be a directory of uid %d with mode 0700", dir, os.Getuid())
	}
	return nil
}
