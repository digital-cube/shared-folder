//go:build unix

package thumbs

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts cmd in a process group of its own and makes the
// cancellation of its context kill the whole group, so helpers pdftoppm
// may start die with it.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
