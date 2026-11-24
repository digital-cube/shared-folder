//go:build !unix

package shfdcli

import (
	"fmt"
	"os"
)

// lockDataRoot is a no-op where flock is not available.
func lockDataRoot(string) (func(), error) { return func() {}, nil }

// lockFile is a no-op where flock is not available.
func lockFile(string, string) (func(), error) { return func() {}, nil }

// checkPrivateDir only checks that dir is a directory where ownership
// cannot be read the unix way.
func checkPrivateDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}
