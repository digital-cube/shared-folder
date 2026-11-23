package client

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Short names for the test tables.
func f(hash string) Entry { return Entry{Kind: File, Hash: hash, Size: int64(len(hash)), Mtime: 1} }

var dir = Entry{Kind: Dir}

func synced(e Entry) ManifestEntry  { return ManifestEntry{Entry: e, State: Synced} }
func skipped(e Entry) ManifestEntry { return ManifestEntry{Entry: e, State: Skipped} }

// show renders a plan compactly: "op path [from] [base=…]".
func show(acts []Action) []string {
	out := []string{}
	for _, a := range acts {
		s := string(a.Op) + " " + a.Path
		if a.From != "" {
			s += " from=" + a.From
		}
		if a.BaseHash != "" {
			s += " base=" + a.BaseHash
		}
		switch a.Op {
		case OpDownload, OpMoveLocal, OpSkip:
			s += " remote=" + a.Remote.Hash
		case OpUpload, OpConflict, OpMove:
			s += " local=" + a.Local.Hash
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

func TestPlanRows(t *testing.T) {
	// One case per row of the ticket's section 3 (and the rows that keep
	// the manifest true), each over the three states of one or two paths.
	cases := []struct {
		name     string
		manifest map[string]ManifestEntry
		local    map[string]Entry
		remote   map[string]Entry
		want     []string
	}{
		{"unchanged everywhere",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": f("h1")}, map[string]Entry{"a": f("h1")},
			[]string{}},
		{"touched locally, same content: manifest takes the new mtime",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": {Kind: File, Hash: "h1", Size: 2, Mtime: 9}}, map[string]Entry{"a": f("h1")},
			[]string{"record a"}},

		// changed only on the server
		{"server: new file → download",
			nil, nil, map[string]Entry{"a": f("h1")},
			[]string{"download a remote=h1"}},
		{"server: edited → download",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": f("h1")}, map[string]Entry{"a": f("h2")},
			[]string{"download a remote=h2"}},
		{"server: deleted → remove locally",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": f("h1")}, nil,
			[]string{"remove-local a"}},
		{"server: moved → rename locally",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": f("h1")}, map[string]Entry{"b": f("h1")},
			[]string{"move-local b from=a remote=h1"}},
		{"server: new directory → mkdir locally",
			nil, nil, map[string]Entry{"d": dir},
			[]string{"mkdir-local d"}},
		{"server: directory removed → rmdir locally",
			map[string]ManifestEntry{"d": synced(dir)}, map[string]Entry{"d": dir}, nil,
			[]string{"rmdir-local d"}},

		// changed only locally
		{"local: new file → put",
			nil, map[string]Entry{"a": f("h1")}, nil,
			[]string{"put a local=h1"}},
		{"local: edited → put with base",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": f("h2")}, map[string]Entry{"a": f("h1")},
			[]string{"put a base=h1 local=h2"}},
		{"local: deleted → delete with base",
			map[string]ManifestEntry{"a": synced(f("h1"))}, nil, map[string]Entry{"a": f("h1")},
			[]string{"delete a base=h1"}},
		{"local: moved (same hash) → move, not delete + put",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"sub/b": f("h1"), "sub": dir}, map[string]Entry{"a": f("h1")},
			[]string{"mkdir sub", "move sub/b from=a base=h1 local=h1"}},
		{"local: new empty directory → mkdir",
			nil, map[string]Entry{"d": dir}, nil,
			[]string{"mkdir d"}},
		{"local: directory removed → delete files, then rmdir",
			map[string]ManifestEntry{"d": synced(dir), "d/x": synced(f("h1"))}, nil, map[string]Entry{"d": dir, "d/x": f("h1")},
			[]string{"delete d/x base=h1", "rmdir d"}},

		// both sides
		{"both: same new content → record only",
			nil, map[string]Entry{"a": f("h1")}, map[string]Entry{"a": f("h1")},
			[]string{"record a"}},
		{"both: edited to the same content → record only",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": f("h2")}, map[string]Entry{"a": f("h2")},
			[]string{"record a"}},
		{"both: deleted → forget",
			map[string]ManifestEntry{"a": synced(f("h1"))}, nil, nil,
			[]string{"forget a"}},
		{"both: edited differently → conflict with the old base",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": f("h2")}, map[string]Entry{"a": f("h3")},
			[]string{"conflict a base=h1 local=h2"}},
		{"both: new, different content → conflict without base",
			nil, map[string]Entry{"a": f("h2")}, map[string]Entry{"a": f("h3")},
			[]string{"conflict a local=h2"}},
		{"deleted locally, edited on the server → download the new version",
			map[string]ManifestEntry{"a": synced(f("h1"))}, nil, map[string]Entry{"a": f("h2")},
			[]string{"download a remote=h2"}},
		{"edited locally, deleted on the server → put, the server restores it",
			map[string]ManifestEntry{"a": synced(f("h1"))}, map[string]Entry{"a": f("h2")}, nil,
			[]string{"put a base=h1 local=h2"}},
		{"file here, directory there → problem",
			nil, map[string]Entry{"a": f("h1")}, map[string]Entry{"a": dir},
			[]string{"problem a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := show(Plan(PlanInput{Manifest: c.manifest, Local: c.local, Remote: c.remote}))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("plan:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func ignoring(paths ...string) func(string, bool) bool {
	return func(p string, _ bool) bool {
		for _, q := range paths {
			if p == q || strings.HasPrefix(p, q+"/") {
				return true
			}
		}
		return false
	}
}

func TestPlanIgnored(t *testing.T) {
	cases := []struct {
		name     string
		manifest map[string]ManifestEntry
		local    map[string]Entry
		remote   map[string]Entry
		want     []string
	}{
		{"ignored local file is not sent",
			nil, map[string]Entry{"x.log": f("h1")}, nil,
			[]string{}},
		{"ignored server file is not fetched, the manifest marks it skipped",
			nil, nil, map[string]Entry{"x.log": f("h1")},
			[]string{"skip x.log remote=h1"}},
		{"ignoring a synced file does not delete it on the server",
			map[string]ManifestEntry{"x.log": synced(f("h1"))}, nil, map[string]Entry{"x.log": f("h1")},
			[]string{"skip x.log remote=h1"}},
		{"ignored local file with the server's name is left alone",
			nil, map[string]Entry{"x.log": f("mine")}, map[string]Entry{"x.log": f("theirs")},
			[]string{"skip x.log remote=theirs"}},
		{"skipped and still ignored: nothing",
			map[string]ManifestEntry{"x.log": skipped(f("h1"))}, nil, map[string]Entry{"x.log": f("h1")},
			[]string{}},
		{"skipped, gone from the server: forget",
			map[string]ManifestEntry{"x.log": skipped(f("h1"))}, nil, nil,
			[]string{"forget x.log"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := show(Plan(PlanInput{Manifest: c.manifest, Local: c.local, Remote: c.remote, Ignored: ignoring("x.log")}))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("plan:\n got %q\nwant %q", got, c.want)
			}
		})
	}

	// When the ignore ends, a skipped row is no base: a missing local
	// file is fetched, not deleted on the server.
	un := []struct {
		name  string
		local map[string]Entry
		want  []string
	}{
		{"missing here → download", nil, []string{"download x.log remote=h1"}},
		{"same content here → record", map[string]Entry{"x.log": f("h1")}, []string{"record x.log"}},
		{"other content here → conflict", map[string]Entry{"x.log": f("h2")}, []string{"conflict x.log local=h2"}},
	}
	for _, c := range un {
		t.Run("unignored: "+c.name, func(t *testing.T) {
			got := show(Plan(PlanInput{
				Manifest: map[string]ManifestEntry{"x.log": skipped(f("h1"))},
				Local:    c.local, Remote: map[string]Entry{"x.log": f("h1")},
			}))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("plan:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestPlanLocalOnlyDir(t *testing.T) {
	m := map[string]ManifestEntry{"build": {Entry: dir, State: LocalOnly}}
	if got := show(Plan(PlanInput{Manifest: m, Local: map[string]Entry{"build": dir}})); len(got) != 0 {
		t.Errorf("a kept directory is not sent back: %q", got)
	}
	if got := show(Plan(PlanInput{Manifest: m})); !reflect.DeepEqual(got, []string{"forget build"}) {
		t.Errorf("kept directory removed here: %q", got)
	}
	got := show(Plan(PlanInput{Manifest: m, Local: map[string]Entry{"build": dir}, Remote: map[string]Entry{"build": dir}}))
	if !reflect.DeepEqual(got, []string{"record build"}) {
		t.Errorf("server has it again: %q", got)
	}
}

func TestPlanCaseConflict(t *testing.T) {
	got := show(Plan(PlanInput{
		Local:  map[string]Entry{"README.md": f("h2"), "Docs": dir, "Docs/a": f("h3"), "readme.md": f("h1"), "docs": dir},
		Remote: map[string]Entry{"readme.md": f("h1"), "docs": dir},
		Manifest: map[string]ManifestEntry{
			"readme.md": synced(f("h1")), "docs": synced(dir),
		},
	}))
	want := []string{"problem Docs", "problem Docs/a", "problem README.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestPlanOrder(t *testing.T) {
	// directories are created shallowest first and removed deepest first;
	// downloads come before uploads, deletions after them
	got := show(Plan(PlanInput{
		Manifest: map[string]ManifestEntry{"old": synced(dir), "old/deep": synced(dir), "gone": synced(f("g"))},
		Local:    map[string]Entry{"n": dir, "n/m": dir, "n/m/f": f("x")},
		Remote:   map[string]Entry{"r": f("y"), "old": dir, "old/deep": dir, "gone": f("g")},
	}))
	want := []string{
		"download r remote=y",
		"mkdir n", "mkdir n/m",
		"put n/m/f local=x",
		"delete gone base=g",
		"rmdir old/deep", "rmdir old",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestPlanMovesPairOneToOne(t *testing.T) {
	// two deleted copies and one new file of the same content: one move,
	// one delete
	got := show(Plan(PlanInput{
		Manifest: map[string]ManifestEntry{"a": synced(f("h")), "b": synced(f("h"))},
		Local:    map[string]Entry{"c": f("h")},
		Remote:   map[string]Entry{"a": f("h"), "b": f("h")},
	}))
	want := []string{"move c from=a base=h local=h", "delete b base=h"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// an edited file that disappears is no move source
	got = show(Plan(PlanInput{
		Manifest: map[string]ManifestEntry{"a": synced(f("h"))},
		Local:    map[string]Entry{"c": f("h")},
		Remote:   map[string]Entry{"a": f("h2")},
	}))
	want = []string{"download a remote=h2", "put c local=h"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	in := PlanInput{Local: map[string]Entry{}, Remote: map[string]Entry{}}
	for i := 0; i < 50; i++ {
		in.Local[fmt.Sprintf("l%02d", i)] = f(fmt.Sprint("l", i))
		in.Remote[fmt.Sprintf("r%02d", i)] = f(fmt.Sprint("r", i))
	}
	first := show(Plan(in))
	for i := 0; i < 5; i++ {
		if got := show(Plan(in)); !reflect.DeepEqual(got, first) {
			t.Fatal("plan order depends on map iteration")
		}
	}
}
