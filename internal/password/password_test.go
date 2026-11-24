package password

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// cheap keeps the tests fast; Default is checked once for its form.
var cheap = Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestHashDefaultForm(t *testing.T) {
	h, err := Hash("tačno-tako-dugačko")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=3,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`).MatchString(h) {
		t.Fatalf("PHC string %q", h)
	}
	ok, rehash, err := Verify("tačno-tako-dugačko", h, Default)
	if err != nil || !ok || rehash {
		t.Fatalf("verify = %v %v %v", ok, rehash, err)
	}
}

func TestVerify(t *testing.T) {
	h, err := HashWith("correct horse battery", cheap)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := HashWith("correct horse battery", cheap)
	if h == h2 {
		t.Error("two hashes of one password are equal: no salt")
	}
	if ok, _, err := Verify("correct horse battery", h, cheap); !ok || err != nil {
		t.Errorf("right password: %v %v", ok, err)
	}
	for _, pw := range []string{"correct horse batter", "Correct horse battery", "", "correct horse battery "} {
		if ok, _, err := Verify(pw, h, cheap); ok || err != nil {
			t.Errorf("wrong password %q: %v %v", pw, ok, err)
		}
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5", "$argon2id$v=19$m=64,t=1$c2FsdHNhbHQ$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1,x=2$c2FsdHNhbHQ$a2V5", "$argon2id$v=19$m=64,t=1,p=1$!!$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$"} {
		if _, _, err := Verify("x", bad, cheap); !errors.Is(err, ErrMalformed) {
			t.Errorf("Verify(%q) err = %v", bad, err)
		}
	}
}

func TestRehash(t *testing.T) {
	weak := Params{Memory: 32, Time: 1, Threads: 1, SaltLen: 8, KeyLen: 16}
	h, _ := HashWith("old enough password", weak)
	for _, want := range []Params{
		{Memory: 64, Time: 1, Threads: 1, SaltLen: 8, KeyLen: 16},
		{Memory: 32, Time: 2, Threads: 1, SaltLen: 8, KeyLen: 16},
		{Memory: 32, Time: 1, Threads: 2, SaltLen: 8, KeyLen: 16},
		{Memory: 32, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 16},
		{Memory: 32, Time: 1, Threads: 1, SaltLen: 8, KeyLen: 32},
	} {
		ok, rehash, err := Verify("old enough password", h, want)
		if !ok || !rehash || err != nil {
			t.Errorf("want %+v: ok=%v rehash=%v err=%v", want, ok, rehash, err)
		}
	}
	// As strong or stronger: no rehash.
	if _, rehash, _ := Verify("old enough password", h, weak); rehash {
		t.Error("rehash of a hash with the wanted parameters")
	}
	if _, rehash, _ := Verify("old enough password", h, Params{Memory: 16, Time: 1, Threads: 1}); rehash {
		t.Error("rehash of a hash stronger than wanted")
	}
	p, err := ParamsOf(h)
	if err != nil || p != weak {
		t.Errorf("ParamsOf = %+v %v", p, err)
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		user, pw string
		ok       bool
	}{
		{"ana", "12345678901", false},                                      // 11
		{"ana", "123456789012", true},                                      // 12
		{"ana", "ššššššššššš", false},                                      // 11 characters, 22 bytes
		{"ana", "šššššššššššš", true},                                      // 12 characters
		{"ana", strings.Repeat("a", 256), true},                            // 256
		{"ana", strings.Repeat("a", 257), false},                           // 257
		{"anastasija12", "anastasija12", false},                            // the username
		{"anastasija12", "AnastasijA12", false},                            // in another case
		{"anastasija12", "anastasija123", true},                            // not the username
		{"ana", "bez ikakvih pravila sastava", true},                       // no composition rules
		{"ana", "\xff\xfe\xfd\xfc\xfb\xfa\xf9\xf8\xf7\xf6\xf5\xf4", false}, // not UTF-8
	} {
		err := Validate(c.user, c.pw)
		if (err == nil) != c.ok {
			t.Errorf("Validate(%q, %q) = %v", c.user, c.pw, err)
		}
	}
}

func TestGenerate(t *testing.T) {
	re := regexp.MustCompile(`^[` + tempAlphabet + `]{5}(-[` + tempAlphabet + `]{5}){3}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p := Generate()
		if !re.MatchString(p) {
			t.Fatalf("Generate() = %q", p)
		}
		if Validate("ana", p) != nil {
			t.Fatalf("Generate() = %q breaks the rules", p)
		}
		if seen[p] {
			t.Fatal("Generate repeated itself")
		}
		seen[p] = true
	}
}
