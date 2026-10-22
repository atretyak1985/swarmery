package ingest

import (
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/workaround"
)

// Workaround-words hook (verify-not-trust phase 5, D23): while the tail stores
// an assistant turn of a phase-linked session, the turn's prose is scanned for
// guards.json's workaroundWords and the hits are written in the SAME
// transaction as the turn — a rolled-back batch leaves no hit behind, and a
// replayed one dedupes on (turn, word, sentence).
//
// What is scanned is assistant-authored text only: text blocks (main chain AND
// subagent sidechains — an interactive run-plan's executors are sidechains,
// whose prose never reaches turns.text) and a subagent's SubagentHandback
// report, which is its final message. Tool output never is. A reviewer
// subagent's prose is skipped: quoting the doc's "bypass" is not changing
// course.

// handbackText is a SubagentHandback's report, "" for any other block.
func handbackText(b contentBlock) string {
	if b.Type != "tool_use" {
		return ""
	}
	return workaround.HandbackReport(b.Name, b.Input)
}

// scanWorkaround stores the hits of one assistant text of turnID. The chain's
// link is resolved once per processRecords call and only when there is prose,
// so a session that is not phase-linked costs one or two index probes per
// batch and no regex work.
func (in *ingester) scanWorkaround(turnID int64, text string, sidechain bool) error {
	if turnID == 0 || text == "" || in.sessionUUID == "" {
		return nil
	}
	if sidechain && workaround.IsReviewer(in.agentName) {
		return nil
	}
	m := workaround.Default()
	if m.Empty() {
		return nil
	}
	if !in.waResolved {
		in.waResolved = true
		l, err := workaround.ResolveLink(in.tx, in.sessionUUID, in.sessionID, in.agentLabel)
		if err != nil {
			return err
		}
		in.waLink = l
	}
	if !in.waLink.Linked {
		return nil
	}
	hits := m.Scan(text)
	if len(hits) == 0 {
		return nil
	}
	agent := ""
	if sidechain {
		agent = in.agentName
	}
	rows := make([]workaround.Row, 0, len(hits))
	for _, h := range hits {
		rows = append(rows, workaround.Row{
			SessionUUID: in.sessionUUID, TurnID: turnID, PhaseID: in.waLink.PhaseID, AgentName: agent, Hit: h,
		})
	}
	_, err := workaround.Insert(in.tx, rows, time.Now())
	return err
}
