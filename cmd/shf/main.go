// Command shf is the shared folder client.
package main

import (
	"os"

	"github.com/digital-cube/shared-folder/internal/cli"
)

func main() {
	p := cli.Program{Name: "shf", Summary: "shared folder client"}
	os.Exit(p.Run(os.Args[1:], os.Stdout, os.Stderr))
}
