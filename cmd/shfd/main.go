// Command shfd is the shared folder server.
package main

import (
	"os"

	"github.com/digital-cube/shared-folder/internal/cli"
)

func main() {
	p := cli.Program{Name: "shfd", Summary: "shared folder server"}
	os.Exit(p.Run(os.Args[1:], os.Stdout, os.Stderr))
}
