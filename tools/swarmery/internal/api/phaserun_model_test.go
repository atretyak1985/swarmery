package api

// The optional {model} body on POST /api/epics/{taskId}/phases/{phaseId}/run.
// The endpoint predates the body, so every shape a caller without one can send —
// no body at all, a zero-length body, "{}", an explicit empty model — must run
// exactly as it always did; only a model outside planning.Models is a 400.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// phaserunModelEnv is internal/phaserun's fallback knob, spelled out here because
// the constant is unexported. Every test below pins it: the daemon these tests
// also run under sets it in its own environment, so leaving it ambient would make
// the "no model anywhere" case assert whatever the operator's plist happens to say.
const phaserunModelEnv = "SWARMERY_PHASERUN_MODEL"

func TestPhaseRun_OptionalBody_StartsRunAsBefore(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string // "" ⇒ no body at all (the pre-change request)
	}{
		{"absent body", ""},
		{"zero-length body", "\x00"}, // replaced below — see the switch
		{"empty object", "{}"},
		{"explicit empty model", `{"model":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(phaserunModelEnv, "")
			srv, db, taskID, _ := epicFixture(t)
			p1, _ := fixturePhaseIDs(t, db, taskID)
			r := &phaseStubRunner{}
			attachPhaseRun(t, db, r, true)

			var resp *http.Response
			switch tc.body {
			case "":
				resp = postPhase(t, phaseRunURL(srv, taskID, p1)) // nil body
			case "\x00":
				resp = postPhaseBody(t, phaseRunURL(srv, taskID, p1), "") // present, empty
			default:
				resp = postPhaseBody(t, phaseRunURL(srv, taskID, p1), tc.body)
			}
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", resp.StatusCode)
			}
			specs := r.dispatchedSpecs()
			if len(specs) != 1 {
				t.Fatalf("dispatched %d runs, want 1", len(specs))
			}
			// No model anywhere ⇒ no --model flag, the account default.
			if specs[0].Model != "" {
				t.Errorf("RunSpec.Model = %q, want empty", specs[0].Model)
			}
		})
	}
}

func TestPhaseRun_BodyModel_ReachesTheSpawn(t *testing.T) {
	// Set, and to a DIFFERENT model: the request's choice outranks the env knob.
	t.Setenv(phaserunModelEnv, "claude-opus-5[1m]")
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	r := &phaseStubRunner{}
	attachPhaseRun(t, db, r, true)

	resp := postPhaseBody(t, phaseRunURL(srv, taskID, p1), `{"model":"sonnet"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	specs := r.dispatchedSpecs()
	if len(specs) != 1 || specs[0].Model != "claude-sonnet-5" {
		t.Fatalf("dispatched specs = %+v, want one with the resolved full ID", specs)
	}
}

func TestPhaseRun_UnknownModel_400_NothingStarted(t *testing.T) {
	t.Setenv(phaserunModelEnv, "")
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	r := &phaseStubRunner{}
	attachPhaseRun(t, db, r, true)

	resp := postPhaseBody(t, phaseRunURL(srv, taskID, p1), `{"model":"gpt-9"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// The message must name what IS accepted — "unknown model" alone leaves the
	// operator guessing at a closed set they cannot see.
	for _, want := range []string{"opus", "sonnet", "fable"} {
		if !strings.Contains(body.Error, want) {
			t.Errorf("error %q does not name %q", body.Error, want)
		}
	}
	if specs := r.dispatchedSpecs(); len(specs) != 0 {
		t.Errorf("dispatched %d runs on a rejected model, want 0", len(specs))
	}
	var state string
	if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "idle" {
		t.Errorf("run_state = %q, want an untouched idle row", state)
	}
}

func TestPhaseRun_MalformedBody_400(t *testing.T) {
	t.Setenv(phaserunModelEnv, "")
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	r := &phaseStubRunner{}
	attachPhaseRun(t, db, r, true)

	resp := postPhaseBody(t, phaseRunURL(srv, taskID, p1), `{"model":`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if specs := r.dispatchedSpecs(); len(specs) != 0 {
		t.Errorf("dispatched %d runs on a malformed body, want 0", len(specs))
	}
}
