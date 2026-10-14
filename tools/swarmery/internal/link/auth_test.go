package link

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }

func call(t *testing.T, h http.HandlerFunc, authz string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/link/events", nil)
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func assertUnauthorized(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if body := rec.Body.String(); body != `{"error":"link token required"}` {
		t.Errorf("body = %s", body)
	}
}

func TestRequireToken(t *testing.T) {
	h := RequireToken(func() string { return "s3cret" })(okHandler)
	for _, tc := range []struct {
		name, authz string
		want        int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"basic scheme", "Basic s3cret", http.StatusUnauthorized},
		{"bare scheme", "Bearer ", http.StatusUnauthorized},
		{"prefix of token", "Bearer s3cre", http.StatusUnauthorized},
		{"right token", "Bearer s3cret", http.StatusAccepted},
		{"case-insensitive scheme", "bearer s3cret", http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, h, tc.authz)
			if tc.want == http.StatusUnauthorized {
				assertUnauthorized(t, rec)
				return
			}
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// A daemon that has no token loaded must refuse everything, including an
// empty credential.
func TestRequireTokenEmptyExpected(t *testing.T) {
	h := RequireToken(func() string { return "" })(okHandler)
	assertUnauthorized(t, call(t, h, "Bearer x"))
}

// Rotation at the token level: the daemon notices a rotated file within one
// reload interval even when every client keeps presenting the OLD token — no
// client has to present the new one first. Inside the interval the cached
// value still answers: the old token passes and the new one is 401.
func TestHolderRotateRevokesOldTokenWithinInterval(t *testing.T) {
	home := t.TempDir()
	old, err := EnsureToken(home)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)
	holder := NewHolder(TokenPath(home), old)
	holder.now = func() time.Time { return clock }
	h := holder.Require()(okHandler)

	if rec := call(t, h, "Bearer "+old); rec.Code != http.StatusAccepted {
		t.Fatalf("old token before rotation: %d", rec.Code)
	}

	fresh, err := Rotate(home)
	if err != nil {
		t.Fatal(err)
	}

	// Same interval as the last reload: the cached old value still answers.
	clock = clock.Add(500 * time.Millisecond)
	if rec := call(t, h, "Bearer "+old); rec.Code != http.StatusAccepted {
		t.Fatalf("old token inside the reload interval: %d, want 202 (cached)", rec.Code)
	}
	assertUnauthorized(t, call(t, h, "Bearer "+fresh))

	// Past the interval: an OLD-token request alone triggers the reload and is
	// refused; nobody presented the new token in between.
	clock = clock.Add(600 * time.Millisecond)
	assertUnauthorized(t, call(t, h, "Bearer "+old))
	if holder.Get() != fresh {
		t.Fatal("holder did not pick up the rotated token from an old-token request")
	}
	if rec := call(t, h, "Bearer "+fresh); rec.Code != http.StatusAccepted {
		t.Fatalf("rotated token: %d, want 202", rec.Code)
	}
	clock = clock.Add(2 * time.Second)
	assertUnauthorized(t, call(t, h, "Bearer "+old))
}

// A request without a bearer never touches the disk: the refresh runs only on
// the comparison path.
func TestHolderRequireSkipsReloadWithoutBearer(t *testing.T) {
	home := t.TempDir()
	tok, err := EnsureToken(home)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)
	holder := NewHolder(TokenPath(home), tok)
	holder.now = func() time.Time { return clock }
	h := holder.Require()(okHandler)

	assertUnauthorized(t, call(t, h, ""))
	if !holder.lastReload.IsZero() {
		t.Fatal("a request without a bearer triggered a reload")
	}
}

// Current over a missing file keeps serving the in-memory value.
func TestHolderCurrentKeepsValueOnMissingFile(t *testing.T) {
	holder := NewHolder(TokenPath(t.TempDir()), "kept")
	h := holder.Require()(okHandler)
	if rec := call(t, h, "Bearer kept"); rec.Code != http.StatusAccepted {
		t.Fatalf("kept token over a missing file: %d, want 202", rec.Code)
	}
}

func TestHolderReloadThrottle(t *testing.T) {
	home := t.TempDir()
	first, err := EnsureToken(home)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 13, 20, 0, 0, 0, time.UTC)
	holder := NewHolder(TokenPath(home), first)
	holder.now = func() time.Time { return clock }

	if holder.Reload() {
		t.Fatal("Reload with an unchanged file reported a change")
	}
	second, err := Rotate(home)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(500 * time.Millisecond)
	if holder.Reload() || holder.Get() != first {
		t.Fatal("Reload inside the 1s window touched the file")
	}
	clock = clock.Add(600 * time.Millisecond)
	if !holder.Reload() || holder.Get() != second {
		t.Fatal("Reload after the window did not pick up the rotated token")
	}
}

// A vanished file keeps the in-memory token: a half-finished rotation never
// locks the mod out.
func TestHolderReloadKeepsValueOnMissingFile(t *testing.T) {
	holder := NewHolder(TokenPath(t.TempDir()), "kept")
	if holder.Reload() || holder.Get() != "kept" {
		t.Fatalf("Reload over a missing file = %q", holder.Get())
	}
}
