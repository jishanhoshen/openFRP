package auth

import (
	"regexp"
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("unexpected hash format: %s", h)
	}
	ok, err := VerifyPassword("correct horse battery staple", h)
	if err != nil || !ok {
		t.Fatalf("verify good password: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong", h)
	if err != nil || ok {
		t.Fatalf("verify bad password: ok=%v err=%v", ok, err)
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Error("hashes must be salted")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, h := range []string{"", "plain", "$bcrypt$x$y$z$w", "$argon2id$v=19$m=1,t=1,p=1$!!$!!"} {
		if _, err := VerifyPassword("x", h); err == nil {
			t.Errorf("expected error for %q", h)
		}
	}
}

func TestTokens(t *testing.T) {
	a, b := NewToken("thk_"), NewToken("thk_")
	if a == b || !strings.HasPrefix(a, "thk_") || len(a) != 4+43 {
		t.Errorf("bad tokens: %q %q", a, b)
	}
	if len(HashToken(a)) != 32 {
		t.Error("hash must be 32 bytes")
	}
}

func TestPairingCode(t *testing.T) {
	re := regexp.MustCompile(`^[A-HJKMNP-Z2-9]{4}-[A-HJKMNP-Z2-9]{4}$`)
	for range 200 {
		if c := NewPairingCode(); !re.MatchString(c) {
			t.Fatalf("bad code %q", c)
		}
	}
	if got := NormalizePairingCode(" abcd efgh "); got != "ABCD-EFGH" {
		t.Errorf("normalize: %q", got)
	}
	if got := NormalizePairingCode("abcd-efgh"); got != "ABCD-EFGH" {
		t.Errorf("normalize: %q", got)
	}
}
