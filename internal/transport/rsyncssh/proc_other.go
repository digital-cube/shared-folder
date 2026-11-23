//go:build !unix

package rsyncssh

import (
	"errors"
	"os/exec"
)

// multiplexSupported: OpenSSH for Windows has no ControlMaster.
const multiplexSupported = false

func setProcessGroup(*exec.Cmd) {}

func checkPrivateDir(string) error { return errors.New("not supported") }
