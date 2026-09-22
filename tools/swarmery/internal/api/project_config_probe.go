package api

// phase: projects — POST /api/projects/{id}/config/{key}/probe asks a live
// `claude` session to discover real values for the fields a pack nominated,
// and returns them as suggestions. It writes nothing.
//
// Why the daemon cannot answer this itself: the honest values live in whatever
// system the pack integrates with, and this daemon holds no credentials for any
// of them — by design, and permanently. What it does have is the same seam the
// auto-provision pipeline uses (provision.Runner), and a `claude` session
// launched through that seam already carries the operator's live connectors. So
// the pack ships a prompt, the daemon hands it over, and the authentication
// stays where it already was. No HTTP client, no token, no domain vocabulary
// enters this file: everything specific is in the pack's requirements.json.
//
// The fence is putProjectConfig's, step for step — SWARMERY_ONBOARD_ROOTS, a
// parseable id, a DECLARED key, a well-formed body, the project row, then
// resolveUnderRoots — because this endpoint spawns a process with the project
// directory as its cwd. That it does not write is not a reason to fence it less.
//
// The one rule this endpoint adds: a probe is never an operator-facing error.
// A timeout, a missing binary, a crashed process, prose instead of JSON, a
// connector that would not resolve — every one of them is 200 with an empty
// suggestions object and a one-line reason. The operator asked for help filling
// in a form; the form still works without the help, and a red 500 over an
// optional convenience would teach them the config page is broken. 5xx is
// unreachable from here on purpose.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/pluginreq"
)

type probeRequest struct {
	// Value is the form's current partial contents — the only thing the daemon
	// substitutes into the pack's prompt.
	Value json.RawMessage `json:"value"`
}

type probeResponse struct {
	// Suggestions is dotted field path → candidate values. NEVER null: the
	// failure shape and the success shape are the same object, so the browser
	// has one code path.
	Suggestions map[string][]string `json:"suggestions"`
	// Reason is set only when the probe produced nothing, and is written to be
	// read by a human in a grey line under the form.
	Reason string `json:"reason,omitempty"`
	// Notes is the agent's own optional remark, passed through untrusted and
	// truncated. Absent far more often than not.
	Notes string `json:"notes,omitempty"`
}

// reasonMaxBytes caps what the daemon will echo of a failure into the modal.
// A stderr tail can be kilobytes of a stack trace; the operator needs the first
// sentence of it and the form needs to stay readable.
const reasonMaxBytes = 240

// probeStdoutLimit caps how much of the agent's stdout is scanned for a JSON
// object. A session told to answer in JSON that instead streams a transcript is
// already a failed probe; reading all of it into the balanced-brace scan would
// only make the failure slower.
const probeStdoutLimit = 1 << 20

// probeOutcome is what runProbe learned: at most one of these is meaningful.
type probeOutcome struct {
	suggestions map[string][]string
	notes       string
	reason      string
}

// probeProjectConfig handles POST /api/projects/{id}/config/{key}/probe.
func (h *Handler) probeProjectConfig(w http.ResponseWriter, r *http.Request) {
	if len(onboardCfg.Roots) == 0 {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{
			"error": "project config probes are disabled — start the daemon with SWARMERY_ONBOARD_ROOTS set to the allowed parent directories",
		})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid project id"}`, http.StatusBadRequest)
		return
	}

	key := r.PathValue("key")
	req, declared, cerr := declaredRequirement(key)
	if errors.Is(cerr, fs.ErrNotExist) {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{
			"error": "swarmery marketplace is not installed on this machine — run a Claude Code marketplace update",
		})
		return
	}
	if cerr != nil {
		writeErr(w, cerr)
		return
	}
	if !declared {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "unknown config key: " + key})
		return
	}
	// A key without a probe block is a 404 rather than an empty 200: nothing
	// declared this endpoint for that key, so answering "no suggestions" would
	// dress a missing route up as a failed lookup. The UI only offers the
	// button when the row carries a probe, so reaching here is a bug, and a bug
	// deserves to be visible.
	if req.Probe == nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{
			"error": "no probe declared for config key: " + key,
		})
		return
	}
	probe := *req.Probe

	var body probeRequest
	if derr := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); derr != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	var value map[string]any
	if uerr := json.Unmarshal(body.Value, &value); uerr != nil || value == nil {
		http.Error(w, `{"error":"value must be a JSON object"}`, http.StatusBadRequest)
		return
	}

	var path string
	err = h.DB.QueryRow(`SELECT path FROM projects WHERE id = ?`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	target, err := resolveUnderRoots(path, onboardCfg.Roots)
	if err != nil {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}

	// Unfilled inputs are a 400, not an empty 200: this is the one probe
	// failure the operator can fix in the form they are already looking at, and
	// spending three minutes of agent time to say so would be worse than rude.
	if missing := probe.MissingNeeds(value); len(missing) > 0 {
		problems := make([]string, 0, len(missing))
		for _, m := range missing {
			problems = append(problems, m+" is required before a probe can run")
		}
		writeJSONStatus(w, http.StatusBadRequest, putConfigInvalid{
			Error:    "probe needs more of " + key + " filled in first",
			Problems: problems,
		})
		return
	}

	out := h.runProbe(r.Context(), target, probe, body.Value)
	writeJSON(w, probeResponse{
		Suggestions: out.suggestions,
		Reason:      out.reason,
		Notes:       out.notes,
	}, nil)
}

// runProbe executes the pack's prompt and reduces whatever comes back to the
// endpoint's contract. It returns a reason instead of an error because there is
// no caller above it that could do anything else with one.
//
// Synchronous on purpose. The provision pipeline is asynchronous because a
// generate step can run for the better part of an hour (internal/provision);
// a probe is a couple of reads with an operator watching a modal. A job row plus
// polling would buy a table and two states in exchange for nothing: the request
// is loopback-only and already bounded by the context deadline — and because
// that deadline is derived from the REQUEST context, an operator who closes the
// modal cancels the process instead of orphaning it.
func (h *Handler) runProbe(parent context.Context, dir string, probe pluginreq.ProbeSpec, value json.RawMessage) probeOutcome {
	if h.Provision == nil || h.Provision.Runner == nil {
		return probeOutcome{suggestions: probe.Trim(nil), reason: "the probe runner is not attached to this daemon"}
	}

	ctx, cancel := context.WithTimeout(parent, probe.Timeout())
	defer cancel()

	// The prompt goes in on STDIN, not argv — the same call shape the provision
	// pipeline uses. Not a stylistic choice: ClaudeRunner folds the joined argv
	// into every error it returns, so a prompt passed as an argument would come
	// back inside the failure text and end up in the grey line under the form.
	stdin := probe.Prompt + "\n\nCurrent partial configuration (JSON):\n" + string(value) + "\n"
	stdout, err := h.Provision.Runner.Claude(ctx, dir, stdin, "-p", "--model", probeModel, "--output-format", "text")
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return probeOutcome{
				suggestions: probe.Trim(nil),
				reason:      fmt.Sprintf("the probe timed out after %s", probe.Timeout()),
			}
		}
		return probeOutcome{suggestions: probe.Trim(nil), reason: shortReason(err.Error())}
	}

	raw := firstJSONObject(stdout)
	if raw == nil {
		return probeOutcome{suggestions: probe.Trim(nil), reason: "the probe did not return JSON"}
	}
	// Strict decode: suggestions must be field → list of strings. A shape we do
	// not recognise is discarded whole rather than half-read, because a
	// half-read suggestion is a value the operator would have no reason to
	// distrust.
	var parsed struct {
		Suggestions map[string][]string `json:"suggestions"`
		Notes       string              `json:"notes"`
	}
	if jerr := json.Unmarshal(raw, &parsed); jerr != nil {
		return probeOutcome{suggestions: probe.Trim(nil), reason: "the probe returned JSON in an unexpected shape"}
	}

	trimmed := probe.Trim(parsed.Suggestions)
	out := probeOutcome{suggestions: trimmed, notes: shortReason(parsed.Notes)}
	if len(trimmed) == 0 {
		out.reason = "the probe found no values for these fields"
	}
	return out
}

// probeModel pins the probe run. Without --model the CLI inherits the account
// default, which is not necessarily the cheap end of the lineup; the provision
// pipeline pins for the same reason. Full ID, not an alias — aliases re-resolve.
const probeModel = "claude-opus-5-5"

// firstJSONObject returns the first balanced {…} in s, or nil.
//
// The agent is instructed to emit JSON and nothing else, and mostly does. But
// "mostly" is not a contract an operator should feel: a session that prefixes
// one line of narration, or wraps the object in a code fence, has still done the
// work, and throwing that away over punctuation would make the feature feel
// unreliable for no reason.
//
// String-aware, so a brace inside a quoted value cannot close the object early —
// the case that turns a lenient scanner into a truncating one. Escapes are
// handled by skipping the next byte, which is enough here: this only has to find
// where the object ENDS, and json.Unmarshal judges the contents afterwards.
func firstJSONObject(s string) json.RawMessage {
	if len(s) > probeStdoutLimit {
		s = s[:probeStdoutLimit]
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil
	}
	depth := 0
	inString := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return json.RawMessage(s[start : i+1])
			}
		}
	}
	return nil
}

// shortReason collapses a message to a single readable line for the modal.
func shortReason(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > reasonMaxBytes {
		s = strings.TrimSpace(s[:reasonMaxBytes]) + "…"
	}
	return s
}
