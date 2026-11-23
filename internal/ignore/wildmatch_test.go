package ignore

import "testing"

// Cases taken from git's t/t3070-wildmatch.sh (the WM_PATHNAME column).
func TestWildmatch(t *testing.T) {
	tests := []struct {
		pat, text string
		want      bool
	}{
		{"foo", "foo", true},
		{"foo", "bar", false},
		{"", "", true},
		{"???", "foo", true},
		{"??", "foo", false},
		{"*", "foo", true},
		{"f*", "foo", true},
		{"*f", "foo", false},
		{"*foo*", "foo", true},
		{"*ob*a*r*", "foobar", true},
		{"*ab", "aaaaaaabababab", true},
		{`foo\*`, "foo*", true},
		{`foo\*bar`, "foobar", false},
		{`f\\oo`, `f\oo`, true},
		{"*[al]?", "ball", true},
		{"[ten]", "ten", false},
		{"**[!te]", "ten", true},
		{"**[!ten]", "ten", false},
		{"t[a-g]n", "ten", true},
		{"t[!a-g]n", "ten", false},
		{"t[!a-g]n", "ton", true},
		{"t[^a-g]n", "ton", true},
		{"a[]]b", "a]b", true},
		{"a[]-]b", "a-b", true},
		{"a[]-]b", "a]b", true},
		{"a[]-]b", "aab", false},
		{"a[]a-]b", "aab", true},
		{"]", "]", true},
		{"foo*bar", "foo/baz/bar", false},
		{"foo**bar", "foo/baz/bar", false},
		{"foo**bar", "foobazbar", true},
		{"foo/**/bar", "foo/baz/bar", true},
		{"foo/**/**/bar", "foo/baz/bar", true},
		{"foo/**/bar", "foo/b/a/z/bar", true},
		{"foo/**/bar", "foo/bar", true},
		{"foo/**/**/bar", "foo/bar", true},
		{"foo?bar", "foo/bar", false},
		{"foo[/]bar", "foo/bar", false},
		{"foo[^a-z]bar", "foo/bar", false},
		{"f[^eiu][^eiu][^eiu][^eiu][^eiu]r", "foo/bar", false},
		{"f[^eiu][^eiu][^eiu][^eiu][^eiu]r", "foo-bar", true},
		{"**/foo", "foo", true},
		{"**/foo", "XXX/foo", true},
		{"**/foo", "bar/baz/foo", true},
		{"*/foo", "bar/baz/foo", false},
		{"**/bar*", "foo/bar/baz", false},
		{"**/bar/*", "deep/foo/bar/baz", true},
		{"**/bar/*", "deep/foo/bar/baz/", false},
		{"**/bar/**", "deep/foo/bar/baz/", true},
		{"**/bar/*", "deep/foo/bar", false},
		{"**/bar/**", "deep/foo/bar/", true},
		{"**/bar**", "foo/bar/baz", false},
		{"*/bar/**", "foo/bar/baz/x", true},
		{"*/bar/**", "deep/foo/bar/baz/x", false},
		{"**/bar/*/*", "deep/foo/bar/baz/x", true},
		{"a/**", "a/b/c", true},
		{"a/**", "a", false},
		{"[[:digit:]]x", "7x", true},
		{"[[:alpha:]][[:upper:]]", "aB", true},
		{"[[:alpha:]][[:upper:]]", "ab", false},
		{"[[:bogus:]]", "a", false},
		{"[abc", "[abc", false}, // unterminated class never matches
		{`foo\`, "foo", false},  // trailing backslash never matches
		{"š*", "šljiva", true},
		{"?ljiva", "šljiva", true},
		{"-*-*-*-*-*-*-12-*-*-*-m-*-*-*", "-adobe-courier-bold-o-normal--12-120-75-75-m-70-iso8859-1", true},
		{"-*-*-*-*-*-*-12-*-*-*-m-*-*-*", "-adobe-courier-bold-o-normal--12-120-75-75-X-70-iso8859-1", false},
		{"XXX/*/*/*/*/*/*/12/*/*/*/m/*/*/*", "XXX/adobe/courier/bold/o/normal//12/120/75/75/m/70/iso8859/1", true},
		{"**/*a*b*g*n*t", "abcd/abcdefg/abcdefghijk/abcdefghijklmnop.txt", true},
		{"**/*a*b*g*n*t", "abcd/abcdefg/abcdefghijk/abcdefghijklmnop.txtz", false},
	}
	for _, tt := range tests {
		if got := wildmatch([]rune(tt.pat), []rune(tt.text)); got != tt.want {
			t.Errorf("wildmatch(%q, %q) = %v, want %v", tt.pat, tt.text, got, tt.want)
		}
	}
}
