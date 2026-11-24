//go:build !unix

package shfcli

import "errors"

func execSelf(string, []string) error {
	return errors.New("shf was updated; start shf daemon again to run the new version")
}
