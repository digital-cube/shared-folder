//go:build unix

package rsyncssh

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts rsync and the ssh it starts into their own process
// group, so cancelling the context stops both.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
}
