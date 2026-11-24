// Package password hashes and checks the passwords of shfd accounts
// (docs/admin.md, "Nalozi"): Argon2id in a PHC string, so the parameters
// travel with the hash and can be raised later (a weaker hash is redone at
// the next sign-in), and the few rules a password must follow.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id parameters of a hash.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32 // passes
	Threads uint8
	SaltLen uint32 // bytes
	KeyLen  uint32 // bytes
}

// Default is what new hashes use: 64 MiB, 3 passes, 1 thread, a 16-byte
// salt and a 32-byte key.
var Default = Params{Memory: 64 * 1024, Time: 3, Threads: 1, SaltLen: 16, KeyLen: 32}

// Rules of a password: its length in characters, and it may not be the
// username.
const (
	MinLen = 12
	MaxLen = 256
)

// ErrMalformed is a stored hash that is not an argon2id PHC string.
var ErrMalformed = errors.New("password hash is not an argon2id PHC string")

// Hash returns the PHC string of pw with the Default parameters.
func Hash(pw string) (string, error) { return HashWith(pw, Default) }

// HashWith returns the PHC string of pw with p:
// $argon2id$v=19$m=65536,t=3,p=1$<salt>$<key>, salt and key in unpadded
// standard base64.
func HashWith(pw string, p Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks pw against a PHC string, comparing the keys in constant
// time. rehash says the hash is weaker than want in some parameter (or
// shorter in salt or key), so it should be replaced by a new one after a
// successful check.
func Verify(pw, phc string, want Params) (ok, rehash bool, err error) {
	p, salt, key, err := decode(phc)
	if err != nil {
		return false, false, err
	}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.Memory, p.Threads, uint32(len(key)))
	ok = subtle.ConstantTimeCompare(got, key) == 1
	rehash = p.Memory < want.Memory || p.Time < want.Time || p.Threads < want.Threads ||
		uint32(len(salt)) < want.SaltLen || uint32(len(key)) < want.KeyLen
	return ok, rehash, nil
}

// ParamsOf reads the parameters of a PHC string.
func ParamsOf(phc string) (Params, error) {
	p, _, _, err := decode(phc)
	return p, err
}

func decode(phc string) (p Params, salt, key []byte, err error) {
	parts := strings.Split(phc, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrMalformed
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, nil, nil, fmt.Errorf("%w: version %q", ErrMalformed, parts[2])
	}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, _ := strings.Cut(kv, "=")
		n, perr := strconv.ParseUint(v, 10, 32)
		if perr != nil {
			return p, nil, nil, fmt.Errorf("%w: parameter %q", ErrMalformed, kv)
		}
		switch k {
		case "m":
			p.Memory = uint32(n)
		case "t":
			p.Time = uint32(n)
		case "p":
			if n > 255 {
				return p, nil, nil, fmt.Errorf("%w: parameter %q", ErrMalformed, kv)
			}
			p.Threads = uint8(n)
		default:
			return p, nil, nil, fmt.Errorf("%w: parameter %q", ErrMalformed, kv)
		}
	}
	if p.Memory == 0 || p.Time == 0 || p.Threads == 0 {
		return p, nil, nil, fmt.Errorf("%w: m, t and p are required", ErrMalformed)
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(salt) == 0 {
		return p, nil, nil, fmt.Errorf("%w: salt", ErrMalformed)
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(key) == 0 {
		return p, nil, nil, fmt.Errorf("%w: key", ErrMalformed)
	}
	p.SaltLen, p.KeyLen = uint32(len(salt)), uint32(len(key))
	return p, salt, key, nil
}

// Validate applies the rules of a new password: MinLen to MaxLen
// characters (any characters) and not the username, in any letter case.
func Validate(username, pw string) error {
	if !utf8.ValidString(pw) {
		return errors.New("the password is not valid UTF-8")
	}
	switch n := utf8.RuneCountInString(pw); {
	case n < MinLen:
		return fmt.Errorf("the password must have at least %d characters", MinLen)
	case n > MaxLen:
		return fmt.Errorf("the password may have at most %d characters", MaxLen)
	}
	if strings.EqualFold(pw, username) {
		return errors.New("the password may not be the username")
	}
	return nil
}

// tempAlphabet leaves out characters that are easy to confuse when a
// password is read out or copied by hand (0/O, 1/l/I).
const tempAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Generate returns a temporary password: 4 groups of 5 characters joined
// by '-', about 115 bits.
func Generate() string {
	var b strings.Builder
	buf := make([]byte, 1)
	for i := 0; i < 20; i++ {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		for {
			if _, err := rand.Read(buf); err != nil {
				panic(err)
			}
			// Rejection sampling keeps every character equally likely.
			if int(buf[0]) < 256-256%len(tempAlphabet) {
				b.WriteByte(tempAlphabet[int(buf[0])%len(tempAlphabet)])
				break
			}
		}
	}
	return b.String()
}
