package linksig

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	vectorKey    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	vectorOrigin = "http://127.0.0.1:7777"
)

// The two vectors are the contract the SPA and the notch implement against:
// changing either output breaks every other client. The unlock vector is
// pinned in tools/notch LinkSignerTests too, and was cross-checked with an
// independent HMAC-SHA256 (Python hmac) over
// "swarmery-unlock\nhttp://127.0.0.1:7777\n1761200000\nn0nce-test-1".
func TestUnlockSigVector(t *testing.T) {
	const want = "99ddc25914ee0a3a46366c5df129d5dfde7fba0f18d0058415d6fd4d11145a02"
	if got := UnlockSig(vectorKey, vectorOrigin, "1761200000", "n0nce-test-1"); got != want {
		t.Fatalf("UnlockSig = %s, want %s", got, want)
	}
	ticket := "1761200000.n0nce-test-1." + want
	nonce, err := VerifyUnlock(vectorKey, vectorOrigin, ticket, time.Unix(1761200000, 0))
	if err != nil || nonce != "n0nce-test-1" {
		t.Fatalf("VerifyUnlock(vector) = %q, %v; want the nonce, nil", nonce, err)
	}
}

func TestSessionSigVector(t *testing.T) {
	const want = "205ee1481fa273c99c904e1a85fa1f8e0ea7876c4d2ebc6870543babba5e690d"
	if got := SessionSig(vectorKey, "1761200000", "r4nd"); got != want {
		t.Fatalf("SessionSig = %s, want %s", got, want)
	}
	if err := VerifySession(vectorKey, "v1.1761200000.r4nd."+want, time.Unix(1761200000, 0)); err != nil {
		t.Fatalf("VerifySession(vector) = %v, want nil", err)
	}
}

func TestUnlockTicketRoundTrip(t *testing.T) {
	now := time.Unix(1761200000, 0)
	ticket, err := UnlockTicket(vectorKey, vectorOrigin, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 || parts[0] != "1761200000" || len(parts[2]) != 64 {
		t.Fatalf("ticket %q: want <ts>.<nonce>.<64 hex>", ticket)
	}
	if _, err := VerifyUnlock(vectorKey, vectorOrigin, ticket, now.Add(119*time.Second)); err != nil {
		t.Errorf("ticket 119 s later: %v, want valid", err)
	}
	if _, err := VerifyUnlock(vectorKey, vectorOrigin, ticket, now.Add(-119*time.Second)); err != nil {
		t.Errorf("ticket 119 s early: %v, want valid", err)
	}
	if _, err := VerifyUnlock(vectorKey, vectorOrigin, ticket, now.Add(121*time.Second)); !errors.Is(err, ErrSkew) {
		t.Errorf("ticket 121 s later: %v, want ErrSkew", err)
	}
	if _, err := VerifyUnlock(vectorKey, vectorOrigin, ticket, now.Add(-121*time.Second)); !errors.Is(err, ErrSkew) {
		t.Errorf("ticket 121 s early: %v, want ErrSkew", err)
	}
	if _, err := VerifyUnlock(strings.Repeat("f", 64), vectorOrigin, ticket, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong key: %v, want ErrBadSignature", err)
	}
	if _, err := VerifyUnlock("", vectorOrigin, ticket, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("empty key: %v, want ErrBadSignature", err)
	}
}

// A ticket is bound to the origin it was minted for: it does not verify for
// any other one, a sibling loopback address or a name included.
func TestUnlockTicketIsOriginBound(t *testing.T) {
	now := time.Unix(1761200000, 0)
	ticket, err := UnlockTicket(vectorKey, vectorOrigin, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{
		"http://127.0.0.1:7778", "http://127.0.0.2:7777", "http://[::1]:7777",
		"https://127.0.0.1:7777", "http://localhost:7777", "",
	} {
		if _, err := VerifyUnlock(vectorKey, other, ticket, now); err == nil {
			t.Errorf("ticket minted for %s verified for %q", vectorOrigin, other)
		}
	}
	if _, err := UnlockTicket(vectorKey, "", now); err == nil {
		t.Error("UnlockTicket with no origin minted a ticket")
	}
}

func TestUnlockOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:7777":      "http://127.0.0.1:7777",
		"http://127.0.0.1:7777/":     "http://127.0.0.1:7777",
		"HTTP://127.0.0.1:7777":      "http://127.0.0.1:7777",
		"http://127.0.0.1":           "http://127.0.0.1:80",
		"https://127.0.0.1":          "https://127.0.0.1:443",
		"http://[::1]:7777":          "http://[::1]:7777",
		"http://[0:0:0:0:0:0:0:1]:7": "http://[::1]:7",
		"http://192.168.1.5:7777":    "http://192.168.1.5:7777",
	} {
		got, err := UnlockOrigin(in)
		if err != nil || got != want {
			t.Errorf("UnlockOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "null", "http://localhost:7777", "http://swarmery:7777", "ftp://127.0.0.1:21",
		"http://[fe80::1%25en0]:7777", "http://[::ffff:127.0.0.1]:7777", "127.0.0.1:7777",
		"http://127.0.0.1:0", "http://127.0.0.1:99999", "http://u@127.0.0.1:7777",
	} {
		if got, err := UnlockOrigin(in); err == nil {
			t.Errorf("UnlockOrigin(%q) = %q, want an error", in, got)
		}
	}
}

func TestVerifyUnlockMalformed(t *testing.T) {
	now := time.Unix(1761200000, 0)
	sig := UnlockSig(vectorKey, vectorOrigin, "1761200000", "n0nce-test-1")
	for name, ticket := range map[string]string{
		"empty":          "",
		"two parts":      "1761200000." + sig,
		"four parts":     "1761200000.n0nce.x." + sig,
		"signed ts":      "+1761200000.n0nce-test-1." + sig,
		"empty nonce":    "1761200000.." + sig,
		"long nonce":     "1761200000." + strings.Repeat("a", 65) + "." + sig,
		"bad nonce char": "1761200000.n0nce/test." + sig,
		"short sig":      "1761200000.n0nce-test-1.abcd",
		"non-hex sig":    "1761200000.n0nce-test-1." + strings.Repeat("z", 64),
	} {
		if _, err := VerifyUnlock(vectorKey, vectorOrigin, ticket, now); err == nil {
			t.Errorf("%s: VerifyUnlock(%q) = nil, want an error", name, ticket)
		}
	}
}

func TestMintSessionRoundTrip(t *testing.T) {
	now := time.Unix(1761200000, 0)
	s, err := MintSession(vectorKey, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != strconv.FormatInt(now.Unix(), 10) || len(parts[2]) != 32 || len(parts[3]) != 64 {
		t.Fatalf("session %q: want v1.<issuedAt>.<32 hex>.<64 hex>", s)
	}
	if err := VerifySession(vectorKey, s, now.Add(SessionTTL)); err != nil {
		t.Errorf("session at exactly 30 days: %v, want valid", err)
	}
	if err := VerifySession(vectorKey, s, now.Add(SessionTTL+time.Second)); err == nil {
		t.Error("session past 30 days verified, want expired")
	}
	if err := VerifySession(vectorKey, s, now.Add(-120*time.Second)); err != nil {
		t.Errorf("session issued 120 s in the future: %v, want valid (clock skew)", err)
	}
	if err := VerifySession(vectorKey, s, now.Add(-121*time.Second)); err == nil {
		t.Error("session issued 121 s in the future verified, want refused")
	}
	if err := VerifySession(strings.Repeat("f", 64), s, now); err == nil {
		t.Error("session under another key verified (a rotated link token must invalidate it)")
	}
	if err := VerifySession("", s, now); err == nil {
		t.Error("session under an empty key verified")
	}
}

func TestVerifySessionMalformed(t *testing.T) {
	now := time.Unix(1761200000, 0)
	sig := SessionSig(vectorKey, "1761200000", "r4nd")
	for name, s := range map[string]string{
		"empty":         "",
		"garbage":       "not-a-session",
		"wrong version": "v2.1761200000.r4nd." + sig,
		"three parts":   "v1.1761200000." + sig,
		"signed ts":     "v1.+1761200000.r4nd." + sig,
		"empty rand":    "v1.1761200000.." + sig,
		"short sig":     "v1.1761200000.r4nd.abcd",
		"tampered rand": "v1.1761200000.r4ne." + sig,
	} {
		if err := VerifySession(vectorKey, s, now); err == nil {
			t.Errorf("%s: VerifySession(%q) = nil, want an error", name, s)
		}
	}
}

func TestUnlockVerifierClaimsOnce(t *testing.T) {
	v := NewUnlockVerifier()
	now := time.Unix(1761200000, 0)
	if !v.Claim("n0nce-test-1", now) {
		t.Fatal("first claim refused")
	}
	if v.Claim("n0nce-test-1", now.Add(2*UnlockMaxSkew-time.Second)) {
		t.Error("a nonce claimed twice inside the ticket window")
	}
	if !v.Claim("n0nce-test-1", now.Add(2*UnlockMaxSkew+time.Second)) {
		t.Error("a nonce is still held after it can no longer verify")
	}
}
