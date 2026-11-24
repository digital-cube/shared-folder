// Package fsinfo tells which file system a path is on and whether it is a
// network one. shfd warns when its databases are on one: SQLite does not
// recommend WAL or POSIX locks on a network file system, and an NFS client
// that stops finishing I/O leaves the folder's writer hanging in the kernel.
package fsinfo

// Info describes the file system a path is on.
type Info struct {
	// Type is a short name: "nfs", "cifs", "smb2", "fuse.sshfs", "ext4",
	// "apfs", ... or "0x<magic>" when the type has no name here.
	Type string
	// Network is true for a file system whose data lives on another
	// machine (NFS, CIFS/SMB, sshfs and the like).
	Network bool
}

// Detector is the type of Detect; tests replace it.
type Detector func(path string) (Info, error)

// Detect reports the file system of path, which must exist.
func Detect(path string) (Info, error) { return detect(path) }
