package pathx

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"
)

func TestNormalizeAccepts(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"slika.png", "slika.png"},
		{"docs/2026/plan.md", "docs/2026/plan.md"},
		{"a//b///c", "a/b/c"},
		{"a/b/", "a/b"},
		{`a\b`, `a\b`}, // backslash is an ordinary character
		{`dir\sub/file`, `dir\sub/file`},
		{".bashrc", ".bashrc"},
		{"...x", "...x"},
		{"a..b", "a..b"},
		{"Čačak/Ђурђевдан.txt", "Čačak/Ђурђевдан.txt"},
		{"CONSOLE.txt", "CONSOLE.txt"}, // only the exact device names are reserved
		{"COM10", "COM10"},
		{"LPT0", "LPT0"},
		{"xCON", "xCON"},
		{"con-tract.pdf", "con-tract.pdf"},
		{" leading space", " leading space"},
		{"~$report.docx", "~$report.docx"},
		{"e\u0301", "\u00e9"}, // NFD input comes back as NFC
	}
	for _, tt := range tests {
		got, err := Normalize(tt.in)
		if err != nil {
			t.Errorf("Normalize(%q): unexpected error %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrEmpty},
		{"only slashes", "//", ErrAbsolute},
		{"invalid utf8", "a/\xff", ErrInvalidUTF8},
		{"absolute", "/etc/passwd", ErrAbsolute},
		{"absolute single", "/a", ErrAbsolute},
		{"dot", ".", ErrDotSegment},
		{"dot segment", "a/./b", ErrDotSegment},
		{"leading dot segment", "./a", ErrDotSegment},
		{"dotdot", "..", ErrDotSegment},
		{"dotdot segment", "a/../b", ErrDotSegment},
		{"dotdot escape", "../etc", ErrDotSegment},
		{"NUL char", "a\x00b", ErrControlChar},
		{"tab", "a\tb", ErrControlChar},
		{"newline", "a/b\n", ErrControlChar},
		{"U+001F", "a\x1fb", ErrControlChar},
		{"DEL", "a\x7fb", ErrControlChar},
		{"lt", "a<b", ErrForbiddenChar},
		{"gt", "a>b", ErrForbiddenChar},
		{"colon", "C:/x", ErrForbiddenChar},
		{"colon in name", "a:b", ErrForbiddenChar},
		{"quote", `a"b`, ErrForbiddenChar},
		{"pipe", "a|b", ErrForbiddenChar},
		{"question", "a?b", ErrForbiddenChar},
		{"star", "a*b", ErrForbiddenChar},
		{"trailing dot", "file.", ErrTrailingDotSpace},
		{"trailing dot dir", "dir./file", ErrTrailingDotSpace},
		{"trailing space", "file ", ErrTrailingDotSpace},
		{"trailing space dir", "dir /file", ErrTrailingDotSpace},
		{"CON", "CON", ErrReservedName},
		{"con lower", "con", ErrReservedName},
		{"Con ext", "Con.txt", ErrReservedName},
		{"con double ext", "con.tar.gz", ErrReservedName},
		{"PRN", "dir/PRN", ErrReservedName},
		{"AUX", "aux.c", ErrReservedName},
		{"NUL", "nul", ErrReservedName},
		{"COM1", "COM1", ErrReservedName},
		{"com9 ext", "com9.log", ErrReservedName},
		{"COM superscript", "COM¹", ErrReservedName},
		{"LPT1", "lpt1", ErrReservedName},
		{"LPT9 ext", "LPT9.txt", ErrReservedName},
		{"reserved dir", "NUL/file.txt", ErrReservedName},
		{"reserved with space before ext", "CON .txt", ErrReservedName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Normalize(%q) = %q, %v; want error %v", tt.in, got, err, tt.want)
			}
			var pe *PathError
			if !errors.As(err, &pe) || pe.Path != tt.in {
				t.Fatalf("error %v is not a *PathError for %q", err, tt.in)
			}
		})
	}
}

func TestNormalizeNFCNFDSamePath(t *testing.T) {
	names := []string{"Ćevapčići/šljiva.txt", "café/résumé.pdf", "Йован.doc"}
	for _, n := range names {
		nfc, nfd := norm.NFC.String(n), norm.NFD.String(n)
		if nfc == nfd {
			t.Fatalf("test data %q has identical NFC and NFD forms", n)
		}
		a, err1 := Normalize(nfc)
		b, err2 := Normalize(nfd)
		if err1 != nil || err2 != nil {
			t.Fatalf("Normalize errors: %v, %v", err1, err2)
		}
		if a != b || a != nfc {
			t.Errorf("NFC %q -> %q, NFD %q -> %q; want both %q", nfc, a, nfd, b, nfc)
		}
	}
}

func TestValidName(t *testing.T) {
	if err := ValidName("ok.txt"); err != nil {
		t.Errorf("ValidName(ok.txt) = %v", err)
	}
	if err := ValidName("a/b"); err == nil {
		t.Error("ValidName(a/b) accepted a separator")
	}
	if err := ValidName("NUL.txt"); !errors.Is(err, ErrReservedName) {
		t.Errorf("ValidName(NUL.txt) = %v", err)
	}
	if err := ValidName(""); !errors.Is(err, ErrEmpty) {
		t.Errorf("ValidName(\"\") = %v", err)
	}
}

func TestFoldKey(t *testing.T) {
	same := [][2]string{
		{"Slika.png", "slika.png"},
		{"SLIKA.PNG", "slika.png"},
		{"Docs/Plan.md", "docs/plan.md"},
		{"ČAČAK.txt", "čačak.txt"},
		{"ĐURĐEVDAN", "đurđevdan"},
		{"ЂОРЂЕ", "ђорђе"},
		{"Straße", "STRASSE"},
		{norm.NFD.String("Šljiva"), "šljiva"}, // NFD input folds to the NFC key
	}
	for _, p := range same {
		if a, b := FoldKey(p[0]), FoldKey(p[1]); a != b {
			t.Errorf("FoldKey(%q) = %q, FoldKey(%q) = %q; want equal", p[0], a, p[1], b)
		}
	}
	different := [][2]string{
		{"slika.png", "slika.jpg"},
		{"a/b", "ab"},
		{"čačak", "cacak"},
	}
	for _, p := range different {
		if a, b := FoldKey(p[0]), FoldKey(p[1]); a == b {
			t.Errorf("FoldKey(%q) == FoldKey(%q) == %q; want different", p[0], p[1], a)
		}
	}
}

var ts = time.Date(2026, 11, 23, 15, 58, 46, 0, time.FixedZone("CET", 3600))

func TestTimestamp(t *testing.T) {
	if got, want := Timestamp(ts), "20261123T145846Z"; got != want {
		t.Errorf("Timestamp = %q, want %q (UTC)", got, want)
	}
}

func TestConflictPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"slika.png", "slika.conflict-ana-20261123T145846Z.png"},
		{"docs/slika.png", "docs/slika.conflict-ana-20261123T145846Z.png"},
		{"Makefile", "Makefile.conflict-ana-20261123T145846Z"},
		{"a/b/README", "a/b/README.conflict-ana-20261123T145846Z"},
		{"arhiva.tar.gz", "arhiva.tar.conflict-ana-20261123T145846Z.gz"},
		{"v1.2.3.notes.txt", "v1.2.3.notes.conflict-ana-20261123T145846Z.txt"},
		{".bashrc", ".bashrc.conflict-ana-20261123T145846Z"},
		{"dir.d/file", "dir.d/file.conflict-ana-20261123T145846Z"},
	}
	for _, tt := range tests {
		got, err := ConflictPath(tt.in, "ana", ts)
		if err != nil {
			t.Errorf("ConflictPath(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ConflictPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if _, err := Normalize(got); err != nil {
			t.Errorf("ConflictPath(%q) = %q is not a valid path: %v", tt.in, got, err)
		}
	}
}

func TestTrashName(t *testing.T) {
	tests := []struct {
		in     string
		reason TrashReason
		want   string
	}{
		{"slika.png", Deleted, "slika.png.deleted-by-ana-20261123T145846Z"},
		{"slika.png", Replaced, "slika.png.replaced-by-ana-20261123T145846Z"},
		{"docs/slika.png", Deleted, "slika.png.deleted-by-ana-20261123T145846Z"},
		{"Makefile", Replaced, "Makefile.replaced-by-ana-20261123T145846Z"},
		{"arhiva.tar.gz", Deleted, "arhiva.tar.gz.deleted-by-ana-20261123T145846Z"},
		{"a/b/.bashrc", Replaced, ".bashrc.replaced-by-ana-20261123T145846Z"},
	}
	for _, tt := range tests {
		got, err := TrashName(tt.in, tt.reason, "ana", ts)
		if err != nil {
			t.Errorf("TrashName(%q, %s): %v", tt.in, tt.reason, err)
			continue
		}
		if got != tt.want {
			t.Errorf("TrashName(%q, %s) = %q, want %q", tt.in, tt.reason, got, tt.want)
		}
		if err := ValidName(got); err != nil {
			t.Errorf("TrashName(%q) = %q is not a valid name: %v", tt.in, got, err)
		}
	}
	if _, err := TrashName("a", "moved", "ana", ts); !errors.Is(err, ErrInvalidReason) {
		t.Errorf("TrashName with bad reason: %v", err)
	}
}

func TestDerivedNamesRejectBadInput(t *testing.T) {
	if _, err := ConflictPath("a.txt", "Ana", ts); !errors.Is(err, ErrInvalidUser) {
		t.Errorf("ConflictPath with bad user: %v", err)
	}
	if _, err := TrashName("a.txt", Deleted, "a/b", ts); !errors.Is(err, ErrInvalidUser) {
		t.Errorf("TrashName with bad user: %v", err)
	}
	if _, err := ConflictPath("", "ana", ts); !errors.Is(err, ErrNoDerivedName) {
		t.Errorf("ConflictPath(\"\"): %v", err)
	}
	if _, err := TrashName("", Deleted, "ana", ts); !errors.Is(err, ErrNoDerivedName) {
		t.Errorf("TrashName(\"\"): %v", err)
	}
}

func TestValidateUsername(t *testing.T) {
	ok := []string{"ana", "a", "igor-p", "pera_2", "0", "abcdefghijklmnopqrstuvwxyz012345"}
	for _, u := range ok {
		if err := ValidateUsername(u); err != nil {
			t.Errorf("ValidateUsername(%q) = %v", u, err)
		}
	}
	bad := []string{
		"", "Ana", "ANA", "ana.b", "ana b", "ana/b", `ana\b`, "šime", "ana:b",
		"abcdefghijklmnopqrstuvwxyz0123456", // 33
	}
	for _, u := range bad {
		if err := ValidateUsername(u); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("ValidateUsername(%q) = %v, want ErrInvalidUser", u, err)
		}
	}
}
