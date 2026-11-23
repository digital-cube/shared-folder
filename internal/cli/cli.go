// Package cli holds the minimal command-line plumbing shared by shfd and shf
// until they grow real subcommands.
package cli

import (
	"fmt"
	"io"

	"github.com/digital-cube/shared-folder/internal/version"
)

// Program describes one binary.
type Program struct {
	Name    string // binary name, e.g. "shfd"
	Summary string // one line: what the binary is
}

// Run handles args (without the program name) and returns the exit code.
// It understands only "version" and "help" for now.
func (p Program) Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		p.usage(stdout)
		return 0
	}
	switch args[0] {
	case "version", "-version", "--version", "-v":
		fmt.Fprintf(stdout, "%s %s\n", p.Name, version.Version)
		return 0
	case "help", "-help", "--help", "-h":
		p.usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n\n", p.Name, args[0])
		p.usage(stderr)
		return 2
	}
}

func (p Program) usage(w io.Writer) {
	fmt.Fprintf(w, `%s — %s

Usage:
  %s <command>

Commands:
  version   print the version
  help      print this help
`, p.Name, p.Summary, p.Name)
}
