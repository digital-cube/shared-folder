package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const shareID = "690a2702-1bb6-4866-b49f-cc8bee54f08e"

func setup(t *testing.T) (Local, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, shareID)
	for _, d := range []string{"storage/docs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return Local{DataRoot: root}, dir
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestLocalSend(t *testing.T) {
	l, dir := setup(t)
	src := filepath.Join(t.TempDir(), "f")
	os.WriteFile(src, []byte("sadržaj"), 0o600)
	id := "0123456789abcdef0123456789abcdef"
	if err := l.Send(context.Background(), Upload{ShareID: shareID, UploadID: id, Dest: "tmp/" + id}, src); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tmp", id)); string(b) != "sadržaj" {
		t.Errorf("tmp content %q", b)
	}
	for _, u := range []Upload{
		{ShareID: shareID, UploadID: id, Dest: "storage/x"},
		{ShareID: shareID, UploadID: "../x", Dest: "tmp/../x"},
		{ShareID: "../" + shareID, UploadID: id, Dest: "tmp/" + id},
	} {
		if err := l.Send(context.Background(), u, src); err == nil {
			t.Errorf("Send(%+v) accepted", u)
		}
	}
}

func TestLocalFetch(t *testing.T) {
	l, dir := setup(t)
	content := []byte("izveštaj")
	os.WriteFile(filepath.Join(dir, "storage/docs/a.txt"), content, 0o600)
	dst := filepath.Join(t.TempDir(), "a.txt")

	if err := l.Fetch(context.Background(), shareID, "docs/a.txt", sum(content), dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != string(content) {
		t.Errorf("fetched %q", b)
	}

	other := filepath.Join(t.TempDir(), "b.txt")
	if err := l.Fetch(context.Background(), shareID, "docs/a.txt", sum([]byte("staro")), other); !errors.Is(err, ErrChanged) {
		t.Errorf("stale hash: %v", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Error("stale fetch left a file")
	}
	if err := l.Fetch(context.Background(), shareID, "docs/nema.txt", sum(content), other); !errors.Is(err, ErrChanged) {
		t.Errorf("missing file: %v", err)
	}
	if err := l.Fetch(context.Background(), shareID, "../registry.db", sum(content), other); err == nil {
		t.Error("path outside storage accepted")
	}
}
