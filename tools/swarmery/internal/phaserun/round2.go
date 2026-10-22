package phaserun

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// The second review round (D42). After the one fix re-run, the reviewer does not
// re-read the whole phase: it is given the previous round's open findings, what
// that round said it did not read (scope_left) and the diff since the tree that
// round reviewed — and asked to answer `round: 2` with `resolves: [ids]`. A
// previous finding the new round neither resolves nor re-reports is not silently
// dropped: the daemon adds a `plan-conformance` finding «unaddressed <id>» to the
// round itself, so the round FAILs on it.

// DetailRound2FullDiff marks a round-2 review whose diff base (the previous
// round's tree_after) was unknown or no longer resolves: the brief carries the
// run's whole diff instead.
const DetailRound2FullDiff = "round2-full-diff"

// round2Brief is what a round-2 reviewer is given in place of the phase doc and
// the run's whole diff.
type round2Brief struct {
	prevRound int
	findings  []mdfence.Finding // the previous round's findings not yet resolved
	scopeLeft []string          // the previous round's scope_left
	diff      string            // the diff section (since the previous round's tree)
	fullDiff  bool              // the diff base was unknown: diff is the run's whole diff
}

// round2Instructions opens the brief: what the round must and must not do.
const round2Instructions = `This is a SECOND review round. Do not re-review the whole phase. Review ONLY:
1. each finding of the previous round listed below — fixed or not;
2. the areas the previous round did not read (its scope_left), if any;
3. the diff below — the change since the tree the previous round reviewed.

In your kind: review block write round: 2. List in resolves the ids of the previous findings you confirmed fixed. A previous finding that still stands is reported again under its own id. A previous finding you neither resolve nor re-report is added back by the daemon as an "unaddressed" plan-conformance finding, and the round fails on it.`

// round2Prompt renders the reviewer's prompt for a round-2 review: the role, the
// round, the phase title and the brief — no phase document, no whole-run diff.
func round2Prompt(title string, b round2Brief, daemonPort, round int) string {
	var s strings.Builder
	s.WriteString(reviewerPreamble(daemonPort))
	s.WriteString("\n\nREVIEW ROUND: ")
	s.WriteString(strconv.Itoa(round))
	s.WriteString("\n\nPHASE: ")
	s.WriteString(title)
	s.WriteString("\n\n")
	s.WriteString(round2Instructions)
	s.WriteString("\n\n")
	s.WriteString(renderRound2Brief(b))
	return s.String()
}

// briefFinding is a previous finding as the brief lists it.
type briefFinding struct {
	ID        string `yaml:"id"`
	Type      string `yaml:"type,omitempty"`
	File      string `yaml:"file,omitempty"`
	Line      int    `yaml:"line,omitempty"`
	Criterion string `yaml:"criterion,omitempty"`
	Evidence  string `yaml:"evidence,omitempty"`
	Note      string `yaml:"note,omitempty"`
}

// renderRound2Brief renders the three inputs of a round-2 review.
func renderRound2Brief(b round2Brief) string {
	var s strings.Builder
	fmt.Fprintf(&s, "PREVIOUS ROUND (%d) FINDINGS:\n", b.prevRound)
	if len(b.findings) == 0 {
		s.WriteString("none open\n")
	} else {
		list := make([]briefFinding, 0, len(b.findings))
		for _, f := range b.findings {
			list = append(list, briefFinding{ID: f.ID, Type: f.Type, File: f.File, Line: f.Line,
				Criterion: f.Criterion, Evidence: f.Evidence, Note: f.Note})
		}
		out, err := yaml.Marshal(list)
		if err != nil {
			for _, f := range b.findings {
				fmt.Fprintf(&s, "- %s %s:%d %s\n", f.ID, f.File, f.Line, f.Evidence)
			}
		} else {
			s.WriteString("```yaml\n")
			s.Write(out)
			s.WriteString("```\n")
		}
	}
	s.WriteString("\nPREVIOUS ROUND SCOPE_LEFT:\n")
	if len(b.scopeLeft) == 0 {
		s.WriteString("none — the previous round covered its scope\n")
	} else {
		for _, l := range b.scopeLeft {
			s.WriteString("- " + l + "\n")
		}
	}
	s.WriteString("\n")
	if b.fullDiff {
		s.WriteString("The tree the previous round reviewed is unknown, so the diff below is the run's whole change.\n")
	}
	s.WriteString(b.diff)
	return s.String()
}

// buildRound2Brief gathers a round-2 review's inputs: the previous round's open
// findings and scope_left (review_findings / review_blocks, else the doc itself
// when the scan has not projected it yet) and the diff from the previous
// review's tree_after to treeNow (this review's tree_before).
func (s *Service) buildRound2Brief(phaseID int64, info phaseInfo, acq worktree.Acquired, prevRound int, treeNow string) round2Brief {
	b := round2Brief{prevRound: prevRound}
	b.findings, b.scopeLeft = s.previousRound(info.DocPath, prevRound)

	base := treeID(s.previousTreeAfter(phaseID))
	now := treeID(treeNow)
	if base != "" && now != "" && s.isTree(acq.Path, base) && s.isTree(acq.Path, now) {
		b.diff = s.diffSection(acq.Path, []string{base, now})
		return b
	}
	b.fullDiff = true
	b.diff = s.reviewDiff(acq.Path, acq.StartPoint)
	return b
}

// treeID is the tree id of a worktree fingerprint ("<tree>" or "<tree>+dirty:…").
func treeID(fingerprint string) string {
	t, _, _ := strings.Cut(strings.TrimSpace(fingerprint), "+dirty:")
	return t
}

// isTree reports whether id names a tree object git can read in dir.
func (s *Service) isTree(dir, id string) bool {
	out, err := s.reviewGit().Run(dir, "cat-file", "-t", id)
	return err == nil && strings.TrimSpace(out) == "tree"
}

// previousTreeAfter is the tree_after of the phase's latest recorded review.
func (s *Service) previousTreeAfter(phaseID int64) string {
	var t string
	err := s.DB.QueryRow(`SELECT COALESCE(tree_after, '') FROM phase_reviews
		 WHERE scope = 'phase' AND phase_id = ? ORDER BY id DESC LIMIT 1`, phaseID).Scan(&t)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("warning: phaserun: phase=%d previous review tree unreadable: %v", phaseID, err)
	}
	return t
}

// previousRound reads round prev of the doc at docPath: its findings no later
// round resolved, and its scope_left. The projection (review_blocks /
// review_findings) is read first; when it does not carry the round yet — the
// wsingest scan runs on its own clock — the doc itself is read.
func (s *Service) previousRound(docPath string, prev int) (findings []mdfence.Finding, scopeLeft []string) {
	round := strconv.Itoa(prev)
	var scopeJSON string
	err := s.DB.QueryRow(`SELECT scope_left_json FROM review_blocks WHERE doc_path = ? AND round = ?`,
		docPath, round).Scan(&scopeJSON)
	if err == nil {
		_ = json.Unmarshal([]byte(scopeJSON), &scopeLeft)
		rows, qerr := s.DB.Query(`SELECT finding_id, COALESCE(type, ''), file, COALESCE(line, 0), criterion, evidence, note
			  FROM review_findings
			 WHERE doc_path = ? AND round = ? AND resolved_by IS NULL
			 ORDER BY id`, docPath, round)
		if qerr == nil {
			defer rows.Close()
			for rows.Next() {
				var f mdfence.Finding
				if rows.Scan(&f.ID, &f.Type, &f.File, &f.Line, &f.Criterion, &f.Evidence, &f.Note) == nil {
					findings = append(findings, f)
				}
			}
			if rows.Err() == nil {
				return findings, scopeLeft
			}
		}
		log.Printf("warning: phaserun: review findings of %s round %s unreadable — reading the doc", docPath, round)
		findings, scopeLeft = nil, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		log.Printf("warning: phaserun: review block of %s round %s unreadable: %v — reading the doc", docPath, round, err)
	}

	body, rerr := os.ReadFile(docPath)
	if rerr != nil {
		return nil, nil
	}
	rs, _ := mdfence.ParseReviews(string(body))
	resolved := map[string]bool{}
	for _, r := range rs {
		if r.Round != round {
			for _, id := range r.Resolves {
				resolved[id] = true
			}
		}
	}
	for _, r := range rs {
		if r.Round != round {
			continue
		}
		scopeLeft = r.ScopeLeft
		for i, f := range r.Findings {
			if strings.TrimSpace(f.ID) == "" {
				f.ID = fmt.Sprintf("R%s-%d", round, i+1)
			}
			if !resolved[f.ID] {
				findings = append(findings, f)
			}
		}
	}
	return findings, scopeLeft
}

// unaddressedFindings is what the daemon adds to a round-2 block: one
// `plan-conformance` finding «unaddressed <id>» per previous finding the round
// neither lists in resolves nor re-reports (same id, its id cited in a new
// finding's evidence or note, or the same file:line).
func unaddressedFindings(prior []mdfence.Finding, r *mdfence.Review, round int) []mdfence.Finding {
	if r == nil {
		return nil
	}
	var out []mdfence.Finding
	for _, p := range prior {
		if slices.Contains(r.Resolves, p.ID) || reReported(p, r.Findings) {
			continue
		}
		out = append(out, mdfence.Finding{
			ID:        fmt.Sprintf("R%d-U%d", round, len(out)+1),
			Type:      "plan-conformance",
			File:      p.File,
			Line:      p.Line,
			Criterion: p.Criterion,
			Evidence:  fmt.Sprintf("unaddressed %s: round %d neither resolved nor re-reported it", p.ID, round),
			Note:      "added by the daemon, not the reviewer. " + p.ID + " was: " + p.Evidence,
		})
	}
	return out
}

// reReported reports whether the new round's findings carry p again.
func reReported(p mdfence.Finding, now []mdfence.Finding) bool {
	for _, f := range now {
		switch {
		case p.ID != "" && f.ID == p.ID:
			return true
		case p.ID != "" && (strings.Contains(f.Evidence, p.ID) || strings.Contains(f.Note, p.ID)):
			return true
		case p.File != "" && p.Line > 0 && f.File == p.File && f.Line == p.Line:
			return true
		}
	}
	return false
}
