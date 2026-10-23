package linksig

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const vectorKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// The two vectors are the contract the SPA and the notch implement against:
// changing either output breaks every other client.
func TestUnlockSigVector(t *testing.T) {
	const want = "6d3d147593d214d11e3b5c8b501651cf6cf188c36cbf8c11523d3c8e42c53f89"
	if got := UnlockSig(vectorKey, "1761200000", "n0nce-test-1"); got != want {
		t.Fatalf("UnlockSig = %s, want %s", got, want)
	}
	ticket := "1761200000.n0nce-test-1." + want
	nonce, err := VerifyUnlock(vectorKey, ticket, time.Unix(1761200000, 0))
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
	ticket, err := UnlockTicket(vectorKey, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 || parts[0] != "1761200000" || len(parts[2]) != 64 {
		t.Fatalf("ticket %q: want <ts>.<nonce>.<64 hex>", ticket)
	}
	if _, err := VerifyUnlock(vectorKey, ticket, now.Add(119*time.Second)); err != nil {
		t.Errorf("ticket 119 s later: %v, want valid", err)
	}
	if _, err := VerifyUnlock(vectorKey, ticket, now.Add(-119*time.Second)); err != nil {
		t.Errorf("ticket 119 s early: %v, want valid", err)
	}
	if _, err := VerifyUnlock(vectorKey, ticket, now.Add(121*time.Second)); !errors.Is(err, ErrSkew) {
		t.Errorf("ticket 121 s later: %v, want ErrSkew", err)
	}
	if _, err := VerifyUnlock(vectorKey, ticket, now.Add(-121*time.Second)); !errors.Is(err, ErrSkew) {
		t.Errorf("ticket 121 s early: %v, want ErrSkew", err)
	}
	if _, err := VerifyUnlock(strings.Repeat("f", 64), ticket, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("wrong key: %v, want ErrBadSignature", err)
	}
	if _, err := VerifyUnlock("", ticket, now); !errors.Is(err, ErrBadSignature) {
		t.Errorf("empty key: %v, want ErrBadSignature", err)
	}
}

func TestVerifyUnlockMalformed(t *testing.T) {
	now := time.Unix(1761200000, 0)
	sig := UnlockSig(vectorKey, "1761200000", "n0nce-test-1")
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
		if _, err := VerifyUnlock(vectorKey, ticket, now); err == nil {
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
