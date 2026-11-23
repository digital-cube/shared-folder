// Command shf is the shared folder client.
package main

import (
	"os"

	"github.com/digital-cube/shared-folder/internal/shfcli"
)

func main() {
	os.Exit(shfcli.Main(os.Args[1:]))
}
