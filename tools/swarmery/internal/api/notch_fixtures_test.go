package api

// Contract pin for the notch menu-bar companion (tools/notch — phase 2):
// decodes the SAME fixture files tools/notch/Tests/SwarmeryNotchTests/
// ModelsTests.swift decodes, but into the LIVE Go DTO structs this package
// serves, with json.Decoder.DisallowUnknownFields(). The Swift client
// deliberately tolerates additive fields (Decodable ignores unknown keys) —
// it can never notice a daemon-side field RENAME on its own. This test can:
// a rename or removal of a field these DTOs serve breaks CI here, before it
// silently breaks the Swift client's decode of the next daemon release.
//
// `go test ./internal/api -run NotchFixtures` selects exactly this file.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// notchFixture reads one fixture file shared with the Swift package. The
// path is relative to this package's directory
// (tools/swarmery/internal/api) up to tools/, then down into
// tools/notch/Tests/Fixtures — the single copy both languages decode.
func notchFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "notch", "Tests", "Fixtures", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return data
}

// decodeStrict decodes data into v with DisallowUnknownFields — a JSON key
// with no matching struct field fails the test, catching a DTO field the
// fixture no longer matches.
func decodeStrict(t *testing.T, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("strict decode into %T: %v", v, err)
	}
}

func TestNotchFixturesSessions(t *testing.T) {
	var page sessionsPageDTO
	decodeStrict(t, notchFixture(t, "sessions.json"), &page)

	if len(page.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(page.Sessions))
	}
	if page.Sessions[0].ID != 2307 {
		t.Errorf("want session[0].ID == 2307, got %d", page.Sessions[0].ID)
	}
	if page.Sessions[0].Status != "active" {
		t.Errorf("want session[0].Status == active, got %q", page.Sessions[0].Status)
	}
	if page.NextCursor != nil {
		t.Errorf("want nextCursor == nil, got %v", *page.NextCursor)
	}
}

func TestNotchFixturesApprovals(t *testing.T) {
	var approvals []permissionRequestDTO
	decodeStrict(t, notchFixture(t, "approvals.json"), &approvals)

	if len(approvals) != 2 {
		t.Fatalf("want 2 approvals, got %d", len(approvals))
	}
	var sawPending bool
	for _, a := range approvals {
		if a.Status == "pending" {
			sawPending = true
			if a.SessionID != 1042 {
				t.Errorf("want pending approval sessionId == 1042, got %d", a.SessionID)
			}
		}
	}
	if !sawPending {
		t.Error("want at least one pending approval in the fixture")
	}
}

func TestNotchFixturesUsage(t *testing.T) {
	var usage usageResp
	decodeStrict(t, notchFixture(t, "usage.json"), &usage)

	if len(usage.Accounts) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(usage.Accounts))
	}
	if len(usage.Providers) == 0 {
		t.Fatal("want at least one provider on the default-account alias")
	}
	if len(usage.Providers[0].Windows) == 0 {
		t.Fatal("want at least one usage window")
	}
}

// wsPermissionRequestedEnvelope pins the WS frame shape (docs/ws-protocol.md)
// with a CONCRETE payload type — unlike the daemon's own wsEnvelope (Payload
// any), so DisallowUnknownFields actually validates the nested
// permissionRequestDTO shape too.
type wsPermissionRequestedEnvelope struct {
	Type    string               `json:"type"`
	Payload permissionRequestDTO `json:"payload"`
}

func TestNotchFixturesWSPermissionRequested(t *testing.T) {
	var frame wsPermissionRequestedEnvelope
	decodeStrict(t, notchFixture(t, "ws-permission_requested.json"), &frame)

	if frame.Type != "permission_requested" {
		t.Errorf("want type == permission_requested, got %q", frame.Type)
	}
	if frame.Payload.Status != "pending" {
		t.Errorf("want payload.status == pending, got %q", frame.Payload.Status)
	}
}

type wsSessionUpdatedEnvelope struct {
	Type    string     `json:"type"`
	Payload sessionDTO `json:"payload"`
}

func TestNotchFixturesWSSessionUpdated(t *testing.T) {
	var frame wsSessionUpdatedEnvelope
	decodeStrict(t, notchFixture(t, "ws-session_updated.json"), &frame)

	if frame.Type != "session_updated" {
		t.Errorf("want type == session_updated, got %q", frame.Type)
	}
	if frame.Payload.ID != 2307 {
		t.Errorf("want payload.id == 2307, got %d", frame.Payload.ID)
	}
}
