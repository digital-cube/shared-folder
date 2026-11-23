//go:build unix

package rsyncssh

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// multiplexSupported: OpenSSH shares connections through unix sockets.
const multiplexSupported = true

// setProcessGroup puts rsync and the ssh it starts into their own process
// group, so cancelling the context stops both. A master connection ssh
// forks for ControlPersist detaches into a session of its own and is not
// stopped with them.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}

// checkPrivateDir accepts dir only when it is a real directory (not a
// symlink) owned by this user and closed to everybody else.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case !ok || int(st.Uid) != os.Getuid():
		return fmt.Errorf("%s is not owned by this user", dir)
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("%s is open to other users (%v)", dir, fi.Mode().Perm())
	}
	return nil
}
