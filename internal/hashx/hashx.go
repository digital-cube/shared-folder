// Package hashx computes the content hash used everywhere in the system:
// SHA-256 of the whole file, as a lowercase hex string
// (docs/specifikacija.md, section 5).
package hashx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// BufferSize is the read buffer size; files are streamed through it and
// never loaded whole.
const BufferSize = 256 << 10

// Empty is the hash of zero bytes.
const Empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// Reader hashes everything r yields until io.EOF.
func Reader(r io.Reader) (string, error) {
	h := sha256.New()
	buf := make([]byte, BufferSize)
	if _, err := io.CopyBuffer(h, onlyReader{r}, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// File hashes the file at path.
func File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum, err := Reader(f)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return sum, nil
}

// onlyReader hides WriterTo (e.g. *os.File) so io.CopyBuffer really uses
// our buffer instead of a strategy chosen by r.
type onlyReader struct{ r io.Reader }

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }
