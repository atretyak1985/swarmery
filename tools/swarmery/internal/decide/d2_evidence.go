package decide

import (
	"database/sql"
	"fmt"
	"log"
	"path"
	"strings"
	"unicode"
)

// d2EvidenceBashRows bounds how many successful git/gh Bash calls one session's
// evidence scan reads. A session past it is long; its counts are a floor.
const d2EvidenceBashRows = 2000

// shipEvidence is D2's deterministic "did the work land" block: commits, push
// and PR activity from successful Bash calls, files edited, the phase run the
// session drove (if any), how the transcript ends (endingLines) and the
// operator's own verdict (if set). The last assistant message alone cannot tell
// a shipped session from an abandoned one; this can.
//
// The git, file and ending lines are rendered from the session's facts, which
// the caller loaded once (d2FactsFor) and the rules read too — nothing is
// queried twice. A DB error never fails labelling: the lines of a part that
// failed to load are dropped (the load already logged why) and the rest is
// still emitted, so the block is never empty.
func shipEvidence(db *sql.DB, f d2Facts, uuid, operatorOutcome string) string {
	var b strings.Builder
	b.WriteString("evidence:\n")
	if f.gitOK {
		fmt.Fprintf(&b, "commits: %d\npushed: %s\npr opened: %d, pr merged: %d\n",
			f.git.commits, yesNo(f.git.pushes > 0), f.git.prsOpened, f.git.prsMerged)
	}
	if f.filesOK {
		fmt.Fprintf(&b, "files edited: %d (+%d/-%d)\n", f.filesEdited, f.additions, f.deletions)
	}
	if line, err := phaseRunLine(db, uuid); err != nil {
		log.Printf("warning: decide: d2 phase-run evidence for %s: %v", uuid, err)
	} else if line != "" {
		b.WriteString(line)
	}
	if f.endingOK {
		b.WriteString(endingLines(f.ending))
	}
	if operatorOutcome != "" {
		fmt.Fprintf(&b, "operator verdict: %s\n", operatorOutcome)
	}
	return b.String()
}

// endingLines says how the transcript ends: how long the session was, whether
// its last assistant turn is the model's own final answer, the stop reason of
// that turn (`unknown` when it was not recorded) and whether the account or the
// API ended it. These are what tell a one-shot question-and-answer session that
// shipped its answer from one that stopped with nothing.
func endingLines(e d2Ending) string {
	stop := strings.TrimSpace(e.stopReason)
	if stop == "" {
		stop = "unknown"
	}
	return fmt.Sprintf("turns: %d\nfinal answer: %s\nlast stop reason: %s\napi error: %s\n",
		e.turns, yesNo(e.finalAnswer()), stop, yesNo(e.apiError()))
}

// gitTally counts the landing actions of a session's successful Bash calls.
type gitTally struct {
	commits, pushes, prsOpened, prsMerged int
}

// gitActivity tallies git commit/push and gh pr create/merge across the
// session's Bash calls that exited ok. An errored call (nothing to commit, a
// rejected push, a failed merge) landed nothing and is not counted.
func gitActivity(db *sql.DB, uuid string) (gitTally, error) {
	var t gitTally
	rows, err := db.Query(`
		SELECT cmd FROM (
		  SELECT COALESCE(json_extract(payload, '$.input.command'), '') AS cmd
		    FROM events
		   WHERE session_id = (SELECT id FROM sessions WHERE session_uuid = ?)
		     AND type = 'tool_call' AND tool_name = 'Bash' AND status = 'ok'
		) WHERE cmd LIKE '%git%' OR cmd LIKE '%gh%'
		LIMIT ?`, uuid, d2EvidenceBashRows)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var cmd string
		if err := rows.Scan(&cmd); err != nil {
			return t, err
		}
		t.add(cmd)
	}
	return t, rows.Err()
}

// add classifies every simple command of one shell command line.
func (t *gitTally) add(cmd string) {
	for _, seg := range shellSegments(cmd) {
		switch landingAction(seg) {
		case "commit":
			t.commits++
		case "push":
			t.pushes++
		case "pr-create":
			t.prsOpened++
		case "pr-merge":
			t.prsMerged++
		}
	}
}

// shellSegments splits a command line on the shell's list and pipe operators
// and newlines. Quoting is ignored: a separator inside a commit message only
// produces a fragment that classifies as nothing.
func shellSegments(cmd string) []string {
	return strings.FieldsFunc(cmd, func(r rune) bool {
		return r == '&' || r == '|' || r == ';' || r == '\n'
	})
}

// landingAction names the landing action a simple command performs: "commit",
// "push", "pr-create", "pr-merge", or "" for anything else. It skips leading
// VAR=value assignments, subshell/group openers and git's global options
// (`git -C dir commit`, `git -c k=v push`). Tokens are split quote-aware, so an
// option value with a space in it (`-c user.name="First Last"`) stays one
// token and the verb after it is still found.
func landingAction(seg string) string {
	f := shellFields(strings.TrimLeft(strings.TrimSpace(seg), "({ "))
	for len(f) > 0 && isAssignment(f[0]) {
		f = f[1:]
	}
	if len(f) < 2 {
		return ""
	}
	switch path.Base(f[0]) {
	case "git":
		switch verb, _ := firstVerb(f[1:], gitArgOpts); verb {
		case "commit", "push":
			return verb
		}
	case "gh":
		if verb, i := firstVerb(f[1:], ghArgOpts); verb == "pr" {
			switch sub, _ := firstVerb(f[i+2:], ghArgOpts); sub {
			case "create":
				return "pr-create"
			case "merge":
				return "pr-merge"
			}
		}
	}
	return ""
}

// shellFields splits a simple command on whitespace that is outside single or
// double quotes. Quotes stay in the token; an unclosed quote (a segment cut
// inside a commit message by shellSegments) runs to the end of the segment.
func shellFields(seg string) []string {
	var out []string
	var quote rune
	start := -1
	for i, r := range seg {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
			if start < 0 {
				start = i
			}
		case unicode.IsSpace(r):
			if start >= 0 {
				out = append(out, seg[start:i])
				start = -1
			}
		default:
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		out = append(out, seg[start:])
	}
	return out
}

// Global options that take a separate value, per CLI.
var (
	gitArgOpts = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true}
	ghArgOpts  = map[string]bool{"-R": true, "--repo": true}
)

// firstVerb is the first non-option token and its index, skipping the value of
// each option in withArg (`-C dir`); `--opt=value` forms carry their own
// value. ("", len(toks)) when there is none.
func firstVerb(toks []string, withArg map[string]bool) (string, int) {
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if !strings.HasPrefix(tok, "-") {
			return tok, i
		}
		if withArg[tok] {
			i++
		}
	}
	return "", len(toks)
}

// isAssignment reports a leading `NAME=value` environment assignment.
func isAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// phaseRunLine describes the plan phase this session ran, or "" when it ran
// none. Criteria prefer the count recorded at the end of the run over the
// doc's current count, which a later run may have moved.
func phaseRunLine(db *sql.DB, uuid string) (string, error) {
	var state string
	var done, total int
	var before, after sql.NullInt64
	err := db.QueryRow(`
		SELECT COALESCE(run_state, ''), checkboxes_done, checkboxes_total, run_checkboxes_before, run_checkboxes_after
		  FROM epic_phases WHERE run_session_uuid = ? ORDER BY id DESC LIMIT 1`, uuid).
		Scan(&state, &done, &total, &before, &after)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if after.Valid {
		done = int(after.Int64)
	}
	line := fmt.Sprintf("phase run: %s, criteria %d/%d ticked", state, done, total)
	if before.Valid {
		line += fmt.Sprintf(" (%d before the run)", before.Int64)
	}
	return line + "\n", nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
