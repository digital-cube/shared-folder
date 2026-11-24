package fsinfo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Magic numbers of statfs(2) f_type (linux/magic.h, fs/*/).
const (
	magicNFS   = 0x6969
	magicSMB   = 0x517b
	magicCIFS  = 0xff534d42
	magicSMB2  = 0xfe534d42
	magicCeph  = 0x00c36400
	magicAFS   = 0x5346414f
	magicCoda  = 0x73757245
	magic9P    = 0x01021997
	magicFUSE  = 0x65735546
	magicExt4  = 0xef53
	magicXFS   = 0x58465342
	magicBtrfs = 0x9123683e
	magicTmpfs = 0x01021994
	magicZFS   = 0x2fc12fc1
	magicOvl   = 0x794c7630
)

var magics = map[int64]Info{
	magicNFS:   {"nfs", true},
	magicSMB:   {"smb", true},
	magicCIFS:  {"cifs", true},
	magicSMB2:  {"smb2", true},
	magicCeph:  {"ceph", true},
	magicAFS:   {"afs", true},
	magicCoda:  {"coda", true},
	magic9P:    {"9p", true},
	magicExt4:  {"ext4", false},
	magicXFS:   {"xfs", false},
	magicBtrfs: {"btrfs", false},
	magicTmpfs: {"tmpfs", false},
	magicZFS:   {"zfs", false},
	magicOvl:   {"overlay", false},
}

// networkFUSE are FUSE subtypes (mountinfo "fuse.<subtype>") that reach
// another machine.
var networkFUSE = map[string]bool{
	"fuse.sshfs": true, "fuse.rclone": true, "fuse.s3fs": true, "fuse.gcsfuse": true,
	"fuse.glusterfs": true, "fuse.ceph-fuse": true, "fuse.davfs": true, "fuse.smbnetfs": true,
}

func detect(path string) (Info, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Info{}, err
	}
	magic := int64(uint32(st.Type))
	if magic == magicFUSE {
		t := "fuse"
		if mt, err := mountType(path); err == nil && mt != "" {
			t = mt
		}
		return Info{Type: t, Network: networkFUSE[t]}, nil
	}
	if in, ok := magics[magic]; ok {
		return in, nil
	}
	return Info{Type: fmt.Sprintf("0x%x", magic)}, nil
}

// mountType is the file system type that /proc/self/mountinfo gives for
// the mount holding path (the longest mount point that is its prefix).
func mountType(path string) (string, error) {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if p, err = filepath.Abs(p); err != nil {
		return "", err
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	defer f.Close()
	return mountTypeFrom(bufio.NewScanner(f), p), nil
}

func mountTypeFrom(sc *bufio.Scanner, p string) string {
	best, typ := -1, ""
	for sc.Scan() {
		// id parent maj:min root mountpoint options [optional...] - fstype source superopts
		pre, post, ok := strings.Cut(sc.Text(), " - ")
		if !ok {
			continue
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) < 5 || len(g) < 1 {
			continue
		}
		mp := unescape(f[4])
		if !within(p, mp) || len(mp) <= best {
			continue
		}
		best, typ = len(mp), g[0]
	}
	return typ
}

func within(p, mp string) bool {
	if mp == "/" {
		return true
	}
	return p == mp || strings.HasPrefix(p, mp+"/")
}

// unescape undoes mountinfo's octal escapes (\040 for a space).
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
