package hashx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

func writeFile(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEmpty(t *testing.T) {
	got, err := File(writeFile(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got != Empty {
		t.Errorf("File(empty) = %s, want %s", got, Empty)
	}
	got, err = Reader(strings.NewReader(""))
	if err != nil || got != Empty {
		t.Errorf("Reader(empty) = %s, %v; want %s", got, err, Empty)
	}
}

func TestKnownVectors(t *testing.T) {
	// FIPS 180-2 test vectors.
	tests := []struct{ in, want string }{
		{"abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
			"248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"},
		{strings.Repeat("a", 1_000_000),
			"cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0"},
	}
	for _, tt := range tests {
		got, err := Reader(strings.NewReader(tt.in))
		if err != nil || got != tt.want {
			t.Errorf("Reader(%.10q…) = %s, %v; want %s", tt.in, got, err, tt.want)
		}
		got, err = File(writeFile(t, []byte(tt.in)))
		if err != nil || got != tt.want {
			t.Errorf("File(%.10q…) = %s, %v; want %s", tt.in, got, err, tt.want)
		}
	}
}

func TestLargerThanBuffer(t *testing.T) {
	data := make([]byte, 3*BufferSize+12345)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])

	got, err := File(writeFile(t, data))
	if err != nil || got != want {
		t.Errorf("File = %s, %v; want %s", got, err, want)
	}
	// Short reads must not change the result.
	short := data[:BufferSize+7]
	for _, tt := range []struct {
		name string
		in   []byte
		r    io.Reader
	}{
		{"one byte", short, iotest.OneByteReader(bytes.NewReader(short))},
		{"half", data, iotest.HalfReader(bytes.NewReader(data))},
		{"data+eof", data, iotest.DataErrReader(bytes.NewReader(data))},
	} {
		s := sha256.Sum256(tt.in)
		got, err := Reader(tt.r)
		if err != nil || got != hex.EncodeToString(s[:]) {
			t.Errorf("%s: Reader = %s, %v", tt.name, got, err)
		}
	}
}

func TestLowercaseHex(t *testing.T) {
	got, _ := Reader(strings.NewReader("x"))
	if len(got) != 64 || strings.ToLower(got) != got {
		t.Errorf("hash %q is not 64 lowercase hex characters", got)
	}
}

func TestErrors(t *testing.T) {
	if _, err := File(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("File(missing) error = %v, want ErrNotExist", err)
	}
	boom := errors.New("boom")
	if _, err := Reader(iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Errorf("Reader(failing) error = %v, want %v", err, boom)
	}
}
