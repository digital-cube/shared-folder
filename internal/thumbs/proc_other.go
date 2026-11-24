//go:build !unix

package thumbs

import "os/exec"

// setProcessGroup leaves cmd as it is where process groups are not
// available; the cancellation kills pdftoppm itself.
func setProcessGroup(*exec.Cmd) {}
