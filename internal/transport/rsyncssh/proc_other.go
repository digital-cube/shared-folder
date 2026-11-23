//go:build !unix

package rsyncssh

import "os/exec"

func setProcessGroup(*exec.Cmd) {}
