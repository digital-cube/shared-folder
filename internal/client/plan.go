package client

import (
	"sort"
	"strings"

	"github.com/digital-cube/shared-folder/internal/pathx"
)

// Kind of a path in one of the three states.
type Kind uint8

const (
	None Kind = iota // absent
	File
	Dir
)

func (k Kind) String() string {
	switch k {
	case File:
		return "file"
	case Dir:
		return "dir"
	}
	return "none"
}

func parseKind(s string) Kind {
	switch s {
	case "file":
		return File
	case "dir":
		return Dir
	}
	return None
}

// Entry is a path as one side sees it. Hash, Size and Mtime (Unix
// nanoseconds) are those of a file; a directory has only its Kind.
type Entry struct {
	Kind  Kind
	Hash  string
	Size  int64
	Mtime int64
}

// same compares content: kind, and the hash of a file. Size and mtime are
// metadata and do not make two entries different.
func same(a, b Entry) bool {
	return a.Kind == b.Kind && (a.Kind != File || a.Hash == b.Hash)
}

// ManifestState says how the manifest relates a path to the server.
type ManifestState string

const (
	// Synced: disk and server agreed on Entry after the last sync.
	Synced ManifestState = "synced"
	// Skipped: the path is ignored here; Entry is what the server has. It
	// is no base for a three-way comparison, so when the ignore ends a
	// missing local file is fetched instead of being taken for a delete.
	Skipped ManifestState = "skipped"
	// LocalOnly: a directory the server removed that stays on disk because
	// it still holds files this client does not send (ignored ones). It is
	// not sent back as a new directory.
	LocalOnly ManifestState = "local"
)

// ManifestEntry is one row of the manifest.
type ManifestEntry struct {
	Entry
	State ManifestState
}

// Op is what an Action does.
type Op string

// Actions that change the server.
const (
	OpUpload   Op = "put"      // send the local file with BaseHash
	OpConflict Op = "conflict" // both sides changed: send with the old BaseHash, the server keeps it as a conflict copy, then fetch the server's version
	OpDelete   Op = "delete"
	OpMove     Op = "move"
	OpMkdir    Op = "mkdir"
	OpRmdir    Op = "rmdir"
)

// Actions that change the local folder.
const (
	OpDownload    Op = "download"
	OpRemoveLocal Op = "remove-local"
	OpMoveLocal   Op = "move-local"
	OpMkdirLocal  Op = "mkdir-local"
	OpRmdirLocal  Op = "rmdir-local"
)

// Actions that change only the manifest.
const (
	OpRecord Op = "record" // disk and server agree: the manifest takes Local (or Remote for a directory)
	OpForget Op = "forget" // drop the manifest row
	OpSkip   Op = "skip"   // the path is ignored: the manifest keeps Remote as skipped
)

// OpProblem is a path that cannot be synchronized until the user acts.
const OpProblem Op = "problem"

// Action is one step of a plan.
type Action struct {
	Op   Op
	Path string
	// From is the source of a move.
	From string
	// BaseHash is the version of the file the client had (put, conflict,
	// delete, move); "" for a new file.
	BaseHash string
	// Local is the local state the action was planned against. Before a
	// local file is replaced or removed the executor checks that the disk
	// still matches it. For a move it is the file at From.
	Local Entry
	// Remote is the server's state the action leads to (download, record).
	Remote Entry
	// Reason explains a problem.
	Reason string
}

// IsBookkeeping reports whether the action touches only the manifest.
func (a Action) IsBookkeeping() bool {
	switch a.Op {
	case OpRecord, OpForget, OpSkip, OpProblem:
		return true
	}
	return false
}

// PlanInput are the three states of the folder.
type PlanInput struct {
	// Manifest is the state after the last successful sync of each path.
	Manifest map[string]ManifestEntry
	// Local is the folder on disk: paths that are not ignored, not
	// symlinks and have names the server accepts.
	Local map[string]Entry
	// Remote is the server's current state.
	Remote map[string]Entry
	// Ignored reports whether a path is out of synchronization here (by
	// .shfignore, or because it is a symlink or has a rejected name).
	// isDir is the kind the path has on whichever side has it.
	Ignored func(p string, isDir bool) bool
}

// Plan compares the three states path by path and returns the actions
// that bring disk and server together, in the order they are to run:
// bookkeeping, local directories, local moves and downloads, uploads and
// remote directories, remote moves, file deletions on both sides, and
// finally directory removals, deepest first.
//
// Per path, with base the manifest entry (when synced):
//
//   - changed only on the server: fetch, remove, move or create locally;
//   - changed only locally: put, delete, move, mkdir or rmdir on the server;
//   - changed on both sides to the same result: record it;
//   - files changed on both sides differently: conflict — the local file
//     goes up with the old base and becomes the server's conflict copy, the
//     server's version is fetched;
//   - deleted locally, changed on the server: fetch the new version;
//   - changed locally, deleted on the server: put it, the server restores it;
//   - ignored: nothing is sent, fetched or removed; the manifest keeps the
//     server's entry as skipped.
//
// A local delete and a local new file with the same hash become one move;
// the same pair coming from the server becomes a local rename.
func Plan(in PlanInput) []Action {
	ignored := in.Ignored
	if ignored == nil {
		ignored = func(string, bool) bool { return false }
	}
	paths := map[string]bool{}
	for p := range in.Manifest {
		paths[p] = true
	}
	for p := range in.Local {
		paths[p] = true
	}
	for p := range in.Remote {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	var acts []Action
	for _, p := range sorted {
		m, hasM := in.Manifest[p]
		l, r := in.Local[p], in.Remote[p]
		isDir := l.Kind == Dir || (l.Kind == None && (r.Kind == Dir || (r.Kind == None && m.Kind == Dir)))
		acts = append(acts, decide(p, m, hasM, l, r, ignored(p, isDir))...)
	}
	acts = caseConflicts(acts, in.Remote)
	acts = pairMoves(acts)
	sort.SliceStable(acts, func(i, j int) bool {
		ri, rj := rank(acts[i]), rank(acts[j])
		if ri != rj {
			return ri < rj
		}
		switch acts[i].Op {
		case OpMkdir, OpMkdirLocal:
			return depth(acts[i].Path) < depth(acts[j].Path)
		case OpRmdir, OpRmdirLocal:
			return depth(acts[i].Path) > depth(acts[j].Path)
		}
		return false
	})
	return acts
}

func rank(a Action) int {
	switch a.Op {
	case OpRecord, OpForget, OpSkip, OpProblem:
		return 0
	case OpMkdirLocal:
		return 1
	case OpMoveLocal:
		return 2
	case OpDownload:
		return 3
	case OpMkdir:
		return 4
	case OpUpload, OpConflict:
		return 5
	case OpMove:
		return 6
	case OpDelete, OpRemoveLocal:
		return 7
	default: // OpRmdir, OpRmdirLocal
		return 8
	}
}

func depth(p string) int { return strings.Count(p, "/") }

func decide(p string, m ManifestEntry, hasM bool, l, r Entry, ign bool) []Action {
	if ign {
		switch {
		case r.Kind != None:
			if !hasM || m.State != Skipped || m.Entry != r {
				return []Action{{Op: OpSkip, Path: p, Remote: r}}
			}
		case hasM:
			return []Action{{Op: OpForget, Path: p}}
		}
		return nil
	}

	var base Entry
	if hasM && m.State == Synced {
		base = m.Entry
	}
	if hasM && m.State == LocalOnly && r.Kind == None {
		if l.Kind == Dir {
			return nil
		}
		return []Action{{Op: OpForget, Path: p}}
	}

	lc, rc := !same(l, base), !same(r, base)
	switch {
	case !lc && !rc:
		if base.Kind == None { // gone everywhere; a skipped row is left over
			if hasM {
				return []Action{{Op: OpForget, Path: p}}
			}
			return nil
		}
		if l.Kind == File && (l.Size != m.Size || l.Mtime != m.Mtime) {
			// same content, touched: remember the new size and mtime so
			// the next scan does not hash it again
			return []Action{{Op: OpRecord, Path: p, Local: l, Remote: r}}
		}
		return nil
	case rc && !lc:
		return pull(p, l, r)
	case lc && !rc:
		return push(p, l, base)
	}
	// changed on both sides
	switch {
	case same(l, r):
		if l.Kind == None {
			return []Action{{Op: OpForget, Path: p}}
		}
		return []Action{{Op: OpRecord, Path: p, Local: l, Remote: r}}
	case l.Kind == None: // deleted here, changed there: the change wins
		return pull(p, l, r)
	case r.Kind == None: // changed here, deleted there: the change wins
		return push(p, l, base)
	case l.Kind == File && r.Kind == File:
		return []Action{{Op: OpConflict, Path: p, BaseHash: base.Hash, Local: l, Remote: r}}
	default:
		return []Action{{Op: OpProblem, Path: p, Local: l, Remote: r,
			Reason: "is a " + l.Kind.String() + " here and a " + r.Kind.String() + " on the server; rename one of them"}}
	}
}

// pull makes the local path what the server has.
func pull(p string, l, r Entry) []Action {
	var out []Action
	switch {
	case l.Kind == File && r.Kind != File:
		out = append(out, Action{Op: OpRemoveLocal, Path: p, Local: l})
	case l.Kind == Dir && r.Kind != Dir:
		out = append(out, Action{Op: OpRmdirLocal, Path: p, Local: l})
	}
	switch r.Kind {
	case File:
		local := l
		if l.Kind == Dir {
			local = Entry{}
		}
		out = append(out, Action{Op: OpDownload, Path: p, Local: local, Remote: r})
	case Dir:
		if l.Kind != Dir {
			out = append(out, Action{Op: OpMkdirLocal, Path: p, Remote: r})
		}
	}
	return out
}

// push makes the server's path what the disk has.
func push(p string, l, base Entry) []Action {
	var out []Action
	switch {
	case base.Kind == File && l.Kind != File:
		out = append(out, Action{Op: OpDelete, Path: p, BaseHash: base.Hash, Local: l})
	case base.Kind == Dir && l.Kind != Dir:
		out = append(out, Action{Op: OpRmdir, Path: p})
	}
	switch l.Kind {
	case File:
		b := ""
		if base.Kind == File {
			b = base.Hash
		}
		out = append(out, Action{Op: OpUpload, Path: p, BaseHash: b, Local: l})
	case Dir:
		if base.Kind != Dir {
			out = append(out, Action{Op: OpMkdir, Path: p, Local: l})
		}
	}
	return out
}

// caseConflicts turns the creation on the server of a name that differs
// from an existing one (or from one created earlier in the same plan) only
// in letter case into a problem: the server would refuse it.
func caseConflicts(acts []Action, remote map[string]Entry) []Action {
	fold := map[string]string{}
	for p := range remote {
		fold[pathx.FoldKey(p)] = p
	}
	for i, a := range acts {
		if a.Op != OpUpload && a.Op != OpMkdir {
			continue
		}
		segs := strings.Split(a.Path, "/")
		for n := 1; n <= len(segs); n++ {
			q := strings.Join(segs[:n], "/")
			k := pathx.FoldKey(q)
			if have, ok := fold[k]; ok && have != q {
				acts[i] = Action{Op: OpProblem, Path: a.Path, Local: a.Local,
					Reason: "differs from " + have + " only in letter case; rename it"}
				break
			}
			if n == len(segs) {
				fold[k] = q
			}
		}
	}
	return acts
}

// pairMoves replaces a delete and a new file of the same content by one
// move, on either side.
func pairMoves(acts []Action) []Action {
	// server side: a local file disappeared (delete with base H) and a new
	// local file with hash H appeared where the server has nothing
	dels := map[string][]int{}
	for i, a := range acts {
		if a.Op == OpDelete && a.Local.Kind == None {
			dels[a.BaseHash] = append(dels[a.BaseHash], i)
		}
	}
	drop := map[int]bool{}
	for i, a := range acts {
		if a.Op != OpUpload || a.BaseHash != "" {
			continue
		}
		if q := dels[a.Local.Hash]; len(q) > 0 {
			d := q[0]
			dels[a.Local.Hash] = q[1:]
			acts[i] = Action{Op: OpMove, Path: a.Path, From: acts[d].Path, BaseHash: a.Local.Hash, Local: a.Local}
			drop[d] = true
		}
	}
	// local side: the server removed a file this client still has
	// unchanged and has the same content at a path that is free here
	rms := map[string][]int{}
	for i, a := range acts {
		if a.Op == OpRemoveLocal && a.Local.Kind == File {
			rms[a.Local.Hash] = append(rms[a.Local.Hash], i)
		}
	}
	for i, a := range acts {
		if a.Op != OpDownload || a.Local.Kind != None {
			continue
		}
		if q := rms[a.Remote.Hash]; len(q) > 0 {
			d := q[0]
			rms[a.Remote.Hash] = q[1:]
			acts[i] = Action{Op: OpMoveLocal, Path: a.Path, From: acts[d].Path, Local: acts[d].Local, Remote: a.Remote}
			drop[d] = true
		}
	}
	if len(drop) == 0 {
		return acts
	}
	out := acts[:0]
	for i, a := range acts {
		if !drop[i] {
			out = append(out, a)
		}
	}
	return out
}
