// Package auth provides password hashing (argon2id) and random secret tokens.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: OWASP minimum recommendation (19 MiB, t=2, p=1).
// Kept modest so a small relay VPS can absorb rate-limited login bursts.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024 // KiB
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

var b64 = base64.RawStdEncoding

// HashPassword returns a PHC-format argon2id hash string.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var ErrBadHash = errors.New("unsupported password hash")

// VerifyPassword checks a password against a PHC argon2id hash, using the
// parameters stored in the hash (so parameters can be raised later).
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, ErrBadHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, ErrBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when a login email doesn't exist, so response
// time doesn't reveal which emails are registered.
var dummyHash, _ = HashPassword("timing-equaliser-not-a-real-password")

func BurnPasswordCheck(password string) { _, _ = VerifyPassword(password, dummyHash) }

// NewToken returns prefix + 32 random bytes (base64url). Prefixes make leaked
// secrets recognisable (and scannable): ths_ session, thk_ API key, thd_ device.
func NewToken(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is used for high-entropy secrets (sessions, API keys, device and
// poll tokens), where a fast hash is appropriate.
func HashToken(token string) []byte {
	s := sha256.Sum256([]byte(token))
	return s[:]
}

// pairing codes: 8 chars from an alphabet without look-alikes (0/O, 1/I/L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// NewPairingCode returns a code formatted as XXXX-XXXX.
func NewPairingCode() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		// 256 % 31 != 0 gives a slight bias; irrelevant for a 10-minute,
		// rate-limited, single-use code.
		out = append(out, codeAlphabet[int(v)%len(codeAlphabet)])
	}
	return string(out)
}

// NormalizePairingCode uppercases and strips spaces/dashes so users can type
// "abcd efgh" or "ABCDEFGH".
func NormalizePairingCode(s string) string {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
	if len(s) == 8 {
		return s[:4] + "-" + s[4:]
	}
	return s
}
