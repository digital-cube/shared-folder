package rsyncssh

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GenerateKey creates a new ed25519 key pair without a passphrase at path
// (and path.pub) with ssh-keygen and returns the public key line, the one
// to register with PUT /v1/shares/{id}/ssh-key.
func GenerateKey(ctx context.Context, path, comment string) (string, error) {
	bin, err := lookPath("", "ssh-keygen")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("rsyncssh: %s already exists", path)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", path).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ssh-keygen: %v: %s", err, strings.TrimSpace(string(out)))
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(pub)), nil
}
