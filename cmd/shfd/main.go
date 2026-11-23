// Command shfd is the shared folder server.
package main

import (
	"os"

	"github.com/digital-cube/shared-folder/internal/shfdcli"
)

func main() {
	os.Exit(shfdcli.Main(os.Args[1:]))
}
