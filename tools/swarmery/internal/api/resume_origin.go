package api

import (
	"database/sql"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/dispatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrun"
)

// A resume is not a new run — it is the SAME conversation, one turn later. But
// until now it was spawned as if it were a stranger: `claude -r <uuid> -p <text>`
// and nothing else. Every flag the original run was given was dropped.
//
// What that costs, concretely:
//
//   - --model. The composer passed none, so the resumed turn ran on the ACCOUNT
//     default (Fable here, ~2× the Opus price) even when the session it is
//     continuing was pinned to Opus by the engine that spawned it. The operator
//     sees one conversation; the bill sees two different models, and the cheaper
//     pin silently became the expensive one on every reply.
//   - --agent. A dispatched task runs as its card's agent; a plan run as its
//     orchestrator. Resume dropped that, so the turn that continues a tech-lead's
//     work arrived as a generic assistant with none of the agent's contract.
//   - --setting-sources. dispatch and verify pin "project,local" to skip the
//     user-level plugin stack. A resume without it loads a different settings
//     stack than the run it is continuing — the same session, two environments.
//
// resumeOrigin is what the resume needs to look like the run it continues. It is
// DERIVED, never stored: the sessions row already carries the model, and which
// engine spawned a session is answerable by the run tables that link to its uuid.
// A second copy on `sessions` would be a column that can disagree with the run
// row it was copied from.
type resumeOrigin struct {
	// Model reaches --model. "" omits the flag (the account default) — only for
	// a session whose row carries no model at all.
	Model string
	// Effort reaches --effort. "" omits the flag.
	Effort string
	// Agent reaches --agent. "" omits the flag.
	Agent string
	// SettingSources reaches --setting-sources. "" omits the flag.
	SettingSources string
	// SettingsFile reaches --settings. "" omits the flag.
	//
	// Not the same decision as SettingSources, and not interchangeable with it:
	// --setting-sources says WHICH tiers of the discovered stack to load, while
	// --settings lends a run a settings file it could never have discovered.
	// dispatch, planrun and phaserun all cut their worktrees under ~/.swarmery,
	// from which nothing walks up to the project's .claude/settings.json, so all
	// three pass one when the run root is a multi-repo sub-repo
	// (repopath.InheritedSettings). A resume that dropped it lost the project's
	// enabled plugins — and with them the agent those plugins ship, which is why
	// an origin that cannot recover its settings file emits no --agent either.
	SettingsFile string
}

// resumeEffortEnv is the resume spawn's --effort knob. Its default is
// deliberately the same as a phase run's rather than something cheaper: a resume
// carries an operator's or a wizard's real instruction — the planning wizard's
// PROCEED turn is a resume, and its product is a whole plan directory — so it is
// continuation of work, not an afterthought.
const (
	resumeEffortEnv = "SWARMERY_RESUME_EFFORT"
	ResumeEffort    = "high"
)

// swarmerySettingSources is what dispatch and verify pin on their own spawns.
// A resume of one of their sessions must load the same stack or it is a
// different environment mid-conversation.
const swarmerySettingSources = "project,local"

// lookupResumeOrigin assembles the flags a resume of sessionUUID should carry.
//
// It degrades rather than fails: this runs on the composer's send path, where a
// query error must cost the operator a less-specific spawn, never the message.
// Every field it cannot determine stays "" and simply omits its flag, which is
// exactly the behaviour every resume had before this function existed.
//
// db may be nil (hermetic handler tests), in which case only the effort default
// is filled.
func lookupResumeOrigin(db *sql.DB, sessionUUID string) resumeOrigin {
	o := resumeOrigin{Effort: claudeflags.Effort(resumeEffortEnv, ResumeEffort)}
	if db == nil || strings.TrimSpace(sessionUUID) == "" {
		return o
	}

	var model, cwd sql.NullString
	// The model the session actually ran on, as ingest recorded it from the
	// transcript. This is the rung that matters most: it is present for EVERY
	// session, swarmery-spawned or not, so even a terminal session an operator
	// started themselves resumes on the model it has been speaking as.
	//
	// cwd comes along because it is the worktree the run was acquired into, and
	// that is the third argument repopath.InheritedSettings needs to answer
	// whether the run was lent a settings file (see ResumeSettingsFile in
	// internal/planrun and internal/phaserun).
	if err := db.QueryRow(
		`SELECT model, cwd FROM sessions WHERE session_uuid = ?`, sessionUUID,
	).Scan(&model, &cwd); err == nil {
		o.Model = stripContextSuffix(model.String)
	}

	// Which engine spawned it. FOUR tables, because four engines stamp a session
	// uuid of their own and each is equally entitled to resume with its original
	// flags — the first cut of this recognised only the first two, which left the
	// most common swarmery session of all (a phase run) resuming as a stranger.
	// A uuid belongs to at most one of them. A miss on all four means a session
	// swarmery did not spawn (an operator's own terminal run): it keeps the model
	// above and takes nothing else, because there is no original argv to match.
	var (
		agent sql.NullString
		one   int
	)
	switch {
	case db.QueryRow(
		`SELECT agent FROM tasks WHERE dispatch_session_uuid = ?`, sessionUUID,
	).Scan(&agent) == nil:
		// dispatch: the card's agent, and dispatch's pinned setting sources — both
		// unconditional, since every dispatch spawn carries "project,local"
		// regardless of whether a settings file was lent. A multi-repo card's
		// worktree IS a sub-repo checkout though, same shape as planrun, so its
		// --agent is only resolvable with the project's settings.json lent
		// alongside it (repopath.InheritedSettings). The two are paired the same
		// way planrun's are: a resume that cannot recover the file drops the
		// agent rather than emit --agent against a worktree with nothing to
		// resolve it.
		o.SettingSources = swarmerySettingSources
		file, ok := dispatch.ResumeSettingsFile(db, sessionUUID, cwd.String)
		if !ok {
			break
		}
		o.Agent = strings.TrimSpace(agent.String)
		o.SettingsFile = file
	case db.QueryRow(
		`SELECT agent FROM plan_runs WHERE run_session_uuid = ?`, sessionUUID,
	).Scan(&agent) == nil:
		// planrun: the orchestrating agent, plus the settings file that makes the
		// agent RESOLVABLE. It passes no --setting-sources of its own, so neither
		// does its resume.
		//
		// The two travel together or not at all. A multi-repo plan run's worktree
		// discovers no .claude/settings.json, so without --settings the session has
		// no core@swarmery and `--agent tech-lead` names an agent that does not
		// exist — a spawn that dies on a flag we added. When the file cannot be
		// recovered the honest resume is the one every resume made before origin
		// flags existed: model only.
		file, ok := planrun.ResumeSettingsFile(db, sessionUUID, cwd.String)
		if !ok {
			break
		}
		o.Agent = strings.TrimSpace(agent.String)
		o.SettingsFile = file
	case db.QueryRow(
		`SELECT 1 FROM epic_phases WHERE run_session_uuid = ?`, sessionUUID,
	).Scan(&one) == nil:
		// phaserun: no --agent and no --setting-sources (phaserun.RunSpec carries
		// neither), but the same lent settings file as a plan run. Nothing here
		// depends on the recovery succeeding, so a miss simply omits the flag.
		o.SettingsFile, _ = phaserun.ResumeSettingsFile(db, sessionUUID, cwd.String)
	case db.QueryRow(
		`SELECT 1 FROM verification_runs WHERE verify_session_uuid = ?`, sessionUUID,
	).Scan(&one) == nil:
		// verify: the read-only judge. Like dispatch it pins the setting sources
		// and lends no settings file; unlike dispatch it runs as no named agent
		// (verify.RunSpec has no Agent field — the contract is the prompt).
		o.SettingSources = swarmerySettingSources
	}
	return o
}

// stripContextSuffix removes a trailing context-window marker from a model id:
// `claude-opus-5-5[1m]` → `claude-opus-5-5`.
//
// Necessary because the two strings serve different purposes. What ingest
// records on `sessions.model` is what the transcript REPORTS, and a run started
// with a 1M-context pin reports the suffixed form. Handing that back to --model
// is not a no-op: the suffix is a request for a different context tier, which the
// CLI either rejects outright or silently re-prices — so a resume that echoed it
// back would change the terms of the conversation it claims to be continuing.
// The bare id is the model; the suffix is a property of how it was launched, and
// re-launching is not this function's decision.
func stripContextSuffix(model string) string {
	m := strings.TrimSpace(model)
	if i := strings.IndexByte(m, '['); i > 0 {
		return strings.TrimSpace(m[:i])
	}
	return m
}
