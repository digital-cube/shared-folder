package ignore

import "unicode"

// wildmatch reports whether text matches the glob pat with gitignore
// semantics (git's wildmatch with WM_PATHNAME):
//
//   - "*" matches any run of characters except "/";
//   - "?" matches one character except "/";
//   - "[...]" matches one character from a set (ranges a-z, negation with
//     "!" or "^", POSIX classes such as [:digit:]); never "/";
//   - "\x" matches x literally;
//   - "**" matches across "/" only when it is a whole segment: a leading
//     "**/", a trailing "/**" or an inner "/**/" (which also matches zero
//     directories). Anywhere else "**" behaves like "*".
//
// Both strings are compared rune by rune, case-sensitively.
func wildmatch(pat, text []rune) bool {
	return match(pat, text) == matched
}

type result int

const (
	matched result = iota
	noMatch
	// abortAll: no later position of the text can match either; lets the
	// "*" loop stop early (as in git).
	abortAll
	// abortToStarStar: only a preceding "**" may still produce a match.
	abortToStarStar
)

func match(pat, t []rune) result { return dowild(pat, 0, t) }

// dowild follows git's wildmatch.c dowild(); pi indexes into pat so that a
// "*" can look at the character before it.
func dowild(pat []rune, pi int, t []rune) result {
	for ; pi < len(pat); pi, t = pi+1, t[1:] {
		pc := pat[pi]
		if len(t) == 0 && pc != '*' {
			return abortAll
		}
		switch pc {
		case '\\':
			pi++
			if pi >= len(pat) { // a trailing backslash never matches
				return noMatch
			}
			if t[0] != pat[pi] {
				return noMatch
			}
		case '?':
			if t[0] == '/' {
				return noMatch
			}
		case '[':
			ok, next, valid := matchClass(pat[pi:], t[0])
			if !valid {
				return abortAll
			}
			if !ok || t[0] == '/' {
				return noMatch
			}
			pi = len(pat) - len(next) - 1 // loop adds 1
		case '*':
			return star(pat, pi, t)
		default:
			if t[0] != pc {
				return noMatch
			}
		}
	}
	if len(t) == 0 {
		return matched
	}
	return noMatch
}

// star handles a run of "*" starting at pat[pi].
func star(pat []rune, pi int, t []rune) result {
	first := pi
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	matchSlash := false
	if pi-first >= 2 {
		segStart := first == 0 || pat[first-1] == '/'
		segEnd := pi == len(pat) || pat[pi] == '/'
		if segStart && segEnd {
			// "/**/" (or a leading "**/") also matches zero directories.
			if pi < len(pat) && dowild(pat, pi+1, t) == matched {
				return matched
			}
			matchSlash = true
		}
	}
	if pi == len(pat) {
		if !matchSlash && containsSlash(t) {
			return noMatch
		}
		return matched
	}
	for len(t) > 0 {
		r := dowild(pat, pi, t)
		if r != noMatch {
			if !matchSlash || r != abortToStarStar {
				return r
			}
		} else if !matchSlash && t[0] == '/' {
			return abortToStarStar
		}
		t = t[1:]
	}
	return abortAll
}

func containsSlash(t []rune) bool {
	for _, r := range t {
		if r == '/' {
			return true
		}
	}
	return false
}

func matchClass(p []rune, c rune) (ok bool, rest []rune, valid bool) {
	i := 1 // skip '['
	negate := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		negate = true
		i++
	}
	first := true
	var prev rune = -1
	for {
		if i >= len(p) {
			return false, nil, false // unterminated class
		}
		ch := p[i]
		if ch == ']' && !first {
			break
		}
		first = false
		switch {
		case ch == '\\':
			i++
			if i >= len(p) {
				return false, nil, false
			}
			ch = p[i]
			if ch == c {
				ok = true
			}
			prev = ch
		case ch == '-' && prev >= 0 && i+1 < len(p) && p[i+1] != ']':
			i++
			hi := p[i]
			if hi == '\\' {
				i++
				if i >= len(p) {
					return false, nil, false
				}
				hi = p[i]
			}
			if prev <= c && c <= hi {
				ok = true
			}
			prev = -1
		case ch == '[' && i+1 < len(p) && p[i+1] == ':':
			end := -1
			for j := i + 2; j+1 < len(p); j++ {
				if p[j] == ':' && p[j+1] == ']' {
					end = j
					break
				}
			}
			if end < 0 {
				// Not a class name: a literal '['.
				if c == '[' {
					ok = true
				}
				prev = ch
				break
			}
			fn, known := posixClasses[string(p[i+2:end])]
			if !known {
				return false, nil, false
			}
			if fn(c) {
				ok = true
			}
			i = end + 1
			prev = -1
		default:
			if ch == c {
				ok = true
			}
			prev = ch
		}
		i++
	}
	return ok != negate, p[i+1:], true
}

var posixClasses = map[string]func(rune) bool{
	"alnum":  func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) },
	"alpha":  unicode.IsLetter,
	"blank":  func(r rune) bool { return r == ' ' || r == '\t' },
	"cntrl":  unicode.IsControl,
	"digit":  unicode.IsDigit,
	"graph":  func(r rune) bool { return unicode.IsGraphic(r) && !unicode.IsSpace(r) },
	"lower":  unicode.IsLower,
	"print":  unicode.IsPrint,
	"punct":  unicode.IsPunct,
	"space":  unicode.IsSpace,
	"upper":  unicode.IsUpper,
	"xdigit": func(r rune) bool { return unicode.Is(unicode.ASCII_Hex_Digit, r) },
}
