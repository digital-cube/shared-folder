package fsinfo

import (
	"strings"
	"syscall"
)

const mntLocal = 0x1000 // MNT_LOCAL of <sys/mount.h>

var networkTypes = map[string]bool{"nfs": true, "smbfs": true, "afpfs": true, "webdav": true, "cifs": true, "ftp": true}

func detect(path string) (Info, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Info{}, err
	}
	t := cstr(st.Fstypename[:])
	from := cstr(st.Mntfromname[:])
	net := networkTypes[t] || st.Flags&mntLocal == 0 ||
		strings.Contains(t, "sshfs") || strings.Contains(from, "sshfs")
	return Info{Type: t, Network: net}, nil
}

func cstr(b []int8) string {
	var s strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		s.WriteByte(byte(c))
	}
	return s.String()
}
