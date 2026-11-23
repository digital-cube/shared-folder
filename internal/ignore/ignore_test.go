package ignore

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/text/unicode/norm"
)

type check struct {
	path  string
	isDir bool
	want  bool
}

func run(t *testing.T, m *Matcher, checks []check) {
	t.Helper()
	for _, c := range checks {
		if got := m.Match(c.path, c.isDir); got != c.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", c.path, c.isDir, got, c.want)
		}
	}
}

func root(content string) *Matcher {
	return New(map[string][]byte{"": []byte(content)})
}

func TestSyntax(t *testing.T) {
	tests := []struct {
		name    string
		content string
		checks  []check
	}{
		{"star", "*.log", []check{
			{"a.log", false, true},
			{"dir/sub/b.log", false, true},
			{"a.log.txt", false, false},
			{"alog", false, false},
		}},
		{"star does not cross slash", "docs/*.md", []check{
			{"docs/a.md", false, true},
			{"docs/sub/a.md", false, false},
		}},
		{"question mark", "file?.txt", []check{
			{"file1.txt", false, true},
			{"fileš.txt", false, true},
			{"file.txt", false, false},
			{"file12.txt", false, false},
		}},
		{"bracket", "img[0-9].png\nx[!a].c\n", []check{
			{"img3.png", false, true},
			{"imgx.png", false, false},
			{"xb.c", false, true},
			{"xa.c", false, false},
		}},
		{"leading double star", "**/cache", []check{
			{"cache", true, true},
			{"a/cache", true, true},
			{"a/b/cache", false, true},
		}},
		{"trailing double star", "build/**", []check{
			{"build", true, false}, // the directory itself is not matched...
			{"build/a", false, true},
			{"build/a/b", false, true},
			{"x/build/a", false, false}, // anchored
		}},
		{"middle double star", "a/**/z", []check{
			{"a/z", false, true},
			{"a/b/z", false, true},
			{"a/b/c/z", false, true},
			{"x/a/b/z", false, false},
		}},
		{"double star not a segment is a star", "a**z", []check{
			{"abcz", false, true},
			{"a/z", false, false},
		}},
		{"trailing slash means directories only", "out/", []check{
			{"out", true, true},
			{"out", false, false},
			{"src/out", true, true},
			{"out/file", false, true}, // via the ignored parent
		}},
		{"leading slash anchors", "/todo.txt", []check{
			{"todo.txt", false, true},
			{"sub/todo.txt", false, false},
		}},
		{"middle slash anchors", "doc/frotz", []check{
			{"doc/frotz", false, true},
			{"a/doc/frotz", false, false},
		}},
		{"no slash matches at any depth", "frotz", []check{
			{"frotz", false, true},
			{"a/frotz", false, true},
			{"a/b/frotz", true, true},
		}},
		{"negation", "*.log\n!keep.log", []check{
			{"a.log", false, true},
			{"keep.log", false, false},
			{"sub/keep.log", false, false},
		}},
		{"last matching pattern wins", "!keep.log\n*.log", []check{
			{"keep.log", false, true},
		}},
		{"comment", "# *.txt\n#a.log", []check{
			{"x.txt", false, false},
			{"#a.log", false, false},
		}},
		{"escaped hash and bang", `\#notes` + "\n" + `\!important`, []check{
			{"#notes", false, true},
			{"!important", false, true},
			{"important", false, false},
		}},
		{"escaped glob characters", `\*.txt` + "\n" + `what\?`, []check{
			{"*.txt", false, true},
			{"a.txt", false, false},
			{"what?", false, true},
			{"whatx", false, false},
		}},
		{"trailing spaces trimmed unless escaped", "a.txt   \nb\\ \n", []check{
			{"a.txt", false, true},
			{"b ", false, true},
			{"b", false, false},
		}},
		{"blank lines and CRLF", "\n\n   \r\n*.bak\r\n\n", []check{
			{"x.bak", false, true},
			{"x", false, false},
		}},
		{"case sensitive", "*.JPG", []check{
			{"a.JPG", false, true},
			{"a.jpg", false, false},
		}},
		{"lone slash and lone bang are ignored", "/\n!\n", []check{
			{"a", false, false},
		}},
		{"BOM", "\xef\xbb\xbf*.o", []check{
			{"a.o", false, true},
		}},
		{"NFD pattern matches NFC path", norm.NFD.String("šljiva.txt"), []check{
			{"šljiva.txt", false, true},
			{norm.NFD.String("šljiva.txt"), false, true},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { run(t, root(tt.content), tt.checks) })
	}
}

func TestNestedDeeperWins(t *testing.T) {
	m := New(map[string][]byte{
		"":         []byte("*.log\nsecret.txt\n"),
		"logs":     []byte("!*.log\n"),
		"logs/old": []byte("*.log\n"),
		"src":      []byte("/gen\n*.txt\n"),
	})
	run(t, m, []check{
		{"a.log", false, true},
		{"logs/a.log", false, false},     // logs/.shfignore re-includes
		{"logs/x/a.log", false, false},   // ...at any depth below it
		{"logs/old/a.log", false, true},  // deeper file wins again
		{"other/a.log", false, true},     // root rule elsewhere
		{"logs/secret.txt", false, true}, // no rule in logs/ for it: root decides
		{"src/gen", true, true},          // anchored to src/
		{"src/x/gen", true, false},
		{"gen", true, false},
		{"src/a.txt", false, true},
		{"a.txt", false, false}, // src/.shfignore does not apply outside src/
		{"logs", true, false},   // a .shfignore does not apply to its own directory
	})
}

func TestNegationUnderIgnoredDirectory(t *testing.T) {
	m := root("build/\n!build/keep.txt\n")
	run(t, m, []check{
		{"build", true, true},
		{"build/keep.txt", false, true}, // cannot re-include under an ignored parent
		{"build/x", false, true},
	})

	// The same from a deeper .shfignore.
	m = New(map[string][]byte{
		"":       []byte("vendor/\n"),
		"vendor": []byte("!keep.txt\n"),
	})
	run(t, m, []check{
		{"vendor/keep.txt", false, true},
	})

	// "dir/**" ignores the contents but not dir itself, so "!" works one
	// level down but not two (exactly like git).
	m = root("dir/**\n!dir/keep.txt\n!dir/sub/keep.txt\n")
	run(t, m, []check{
		{"dir", true, false},
		{"dir/keep.txt", false, false},
		{"dir/other", false, true},
		{"dir/sub", true, true},
		{"dir/sub/keep.txt", false, true},
	})

	// Classic whitelist: ignore everything, re-include directories and *.go.
	m = root("*\n!*/\n!*.go\n")
	run(t, m, []check{
		{"a/b/main.go", false, false},
		{"a/b/readme.md", false, true},
		{"a", true, false},
	})
}

func TestShfignoreIgnoresItself(t *testing.T) {
	m := root(".shfignore\n")
	run(t, m, []check{
		{".shfignore", false, true},
		{"sub/.shfignore", false, true},
	})
	m = New(map[string][]byte{"sub": []byte("/.shfignore\n")})
	run(t, m, []check{
		{"sub/.shfignore", false, true},
		{".shfignore", false, false},
	})
	run(t, root("*.tmp"), []check{{".shfignore", false, false}})
}

func TestDefaultPatterns(t *testing.T) {
	m := New(nil)
	run(t, m, []check{
		{".DS_Store", false, true},
		{"a/b/.DS_Store", false, true},
		{"Thumbs.db", false, true},
		{"photos/desktop.ini", false, true},
		{"~$report.docx", false, true},
		{".main.go.swp", false, true},
		{"x.tmp", false, true},
		{"movie.mkv.part", false, true},
		{"file.zip.crdownload", false, true},
		{"report.docx", false, false},
		{"thumbs.db", false, false}, // case-sensitive, like git
		{"cache.tmp", true, true},
		{"cache.tmp/inside.txt", false, true},
	})
}

func TestDefaultPatternsOverride(t *testing.T) {
	m := root("!*.tmp\n!desktop.ini\n")
	run(t, m, []check{
		{"x.tmp", false, false},
		{"a/x.tmp", false, false},
		{"desktop.ini", false, false},
		{".DS_Store", false, true}, // not overridden
	})
	// An override in a subfolder only applies below it.
	m = New(map[string][]byte{"data": []byte("!*.part\n")})
	run(t, m, []check{
		{"data/a.part", false, false},
		{"a.part", false, true},
	})
	// A user pattern that ignores more still works with the defaults.
	run(t, root("*.log\n"), []check{{"a.log", false, true}, {"a.tmp", false, true}})
}

func TestClientTempCannotBeOverridden(t *testing.T) {
	for _, content := range []string{"!.shf-tmp-*\n", "!*\n", "!**\n!*/\n"} {
		m := root(content)
		run(t, m, []check{
			{".shf-tmp-123", false, true},
			{"a/b/.shf-tmp-x.png", false, true},
			{".shf-tmp-dir/file", false, true},
		})
	}
	m := New(map[string][]byte{"a": []byte("!.shf-tmp-*\n")})
	run(t, m, []check{{"a/.shf-tmp-1", false, true}})
	run(t, New(nil), []check{{"shf-tmp-1", false, false}, {"x.shf-tmp-1", false, false}})
}

func TestRootNeverIgnored(t *testing.T) {
	run(t, root("*\n"), []check{{"", true, false}, {"x", false, true}})
}

func TestWith(t *testing.T) {
	base := root("*.log\n")
	next := base.With("", []byte("*.log\n!keep.log\n"))
	run(t, base, []check{{"keep.log", false, true}})
	run(t, next, []check{{"keep.log", false, false}})

	added := base.With("sub", []byte("*.txt\n"))
	run(t, added, []check{{"sub/a.txt", false, true}, {"a.txt", false, false}})
	run(t, base, []check{{"sub/a.txt", false, false}})

	removed := added.With("sub/", nil)
	run(t, removed, []check{{"sub/a.txt", false, false}, {"a.log", false, true}})

	dot := New(map[string][]byte{".": []byte("x\n")})
	run(t, dot, []check{{"x", false, true}})
}

func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".shfignore", "*.log\nignored/\n")
	write("logs/.shfignore", "!*.log\n")
	write("logs/deep/.shfignore", "*.log\n")
	write("ignored/.shfignore", "!*\n") // never read: its folder is ignored
	write(norm.NFD.String("šuma")+"/.shfignore", "*.txt\n")
	if err := os.MkdirAll(filepath.Join(dir, "dironly", ".shfignore"), 0o755); err != nil {
		t.Fatal(err) // a directory named .shfignore holds no rules
	}

	m, err := FromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	run(t, m, []check{
		{"a.log", false, true},
		{"logs/a.log", false, false},
		{"logs/deep/a.log", false, true},
		{"ignored/x", false, true},
		{"šuma/a.txt", false, true}, // directory read in NFD, rules keyed in NFC
		{"a.txt", false, false},
		{"dironly/x", false, false},
	})
	if _, ok := m.files["ignored"]; ok {
		t.Error("FromDir read a .shfignore inside an ignored directory")
	}

	if _, err := FromDir(filepath.Join(dir, "missing")); err == nil {
		t.Error("FromDir(missing) returned no error")
	}
}
