package fsinfo

import (
	"bufio"
	"strings"
	"testing"
)

const mountinfo = `22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw
40 22 0:35 / /disk2 rw,relatime shared:20 - nfs4 fs:/export rw,vers=4.2
41 22 0:36 / /mnt/remote\040box rw,nosuid shared:21 - fuse.sshfs ana@box: rw,user_id=0
42 22 0:37 / /mnt/enc rw,nosuid shared:22 - fuse.gocryptfs /srv/enc rw
`

func TestMountTypeFrom(t *testing.T) {
	for p, want := range map[string]string{
		"/var/lib/shfd/db":        "ext4",
		"/disk2/shared-storage":   "nfs4",
		"/disk2":                  "nfs4",
		"/disk22":                 "ext4",
		"/mnt/remote box/x":       "fuse.sshfs",
		"/mnt/enc/shared-storage": "fuse.gocryptfs",
	} {
		if got := mountTypeFrom(bufio.NewScanner(strings.NewReader(mountinfo)), p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
	if !networkFUSE["fuse.sshfs"] || networkFUSE["fuse.gocryptfs"] {
		t.Error("networkFUSE")
	}
}

func TestMagics(t *testing.T) {
	for m, net := range map[int64]bool{magicNFS: true, magicCIFS: true, magicSMB2: true, magicSMB: true, magicExt4: false, magicXFS: false} {
		if magics[m].Network != net {
			t.Errorf("0x%x: network %v", m, magics[m].Network)
		}
	}
}

func TestDetectLocal(t *testing.T) {
	in, err := Detect(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if in.Type == "" {
		t.Errorf("no type: %+v", in)
	}
	if _, err := Detect("/nonexistent/x"); err == nil {
		t.Error("no error for a missing path")
	}
}
