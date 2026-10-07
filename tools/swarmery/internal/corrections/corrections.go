// Package corrections is the operator correction ledger (memory-engineering
// gaps, phase 3; migration 0102).
//
// Every surface where a person overrides what an agent produced — a lesson
// rewritten, dismissed or retired by hand, a triage verdict undone, a plan
// revision rejected, a classifier answer contradicted by ground truth — already
// exists and already works. What none of them did was COUNT: each correction
// was absorbed by its own feature and forgotten, so the same mistake could be
// corrected by hand a dozen times without anything noticing. This package is
// the one place those corrections land, and advisor R14 is the reader that
// turns a repeat into a recommendation.
//
// Two rules the callers rely on:
//
//   - Record is called from the operator ENDPOINT, never from the domain
//     function it wraps. lessons.Retire is also the verifier's auto-retire; a
//     recorder inside it would count the daemon correcting itself.
//   - A failed ledger write must never fail the operator's action. Record
//     logs the failure and returns it so tests can see it; HTTP handlers call
//     it after their own write succeeded and discard the result.
package corrections

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Sources — the CHECK vocabulary of operator_corrections.source.
const (
	SourceLessonEdit     = "lesson_edit"
	SourceLessonDismiss  = "lesson_dismiss"
	SourceLessonRetire   = "lesson_retire"
	SourceTriageUndo     = "triage_undo"
	SourceRevisionReject = "revision_reject"
	SourceTruthDisagree  = "truth_disagree"
)

var sources = map[string]bool{
	SourceLessonEdit: true, SourceLessonDismiss: true, SourceLessonRetire: true,
	SourceTriageUndo: true, SourceRevisionReject: true, SourceTruthDisagree: true,
}

// tsFmt matches the ingest/advisor timestamp shape so the advisor's string
// window predicates (fmtTS) compare correctly against created_at.
const tsFmt = "2006-01-02T15:04:05.000Z"

// ErrInvalid is returned for a correction that cannot be stored as written.
var ErrInvalid = errors.New("corrections: invalid correction")

// Correction is one operator override as the endpoint saw it.
type Correction struct {
	Source    string
	Ref       string // 'lesson:12' | 'verdict:88' | 'revision:5' | 'decision:1947'
	ProjectID *int64 // nil when the handler does not know it
	Before    string // what the agent produced
	After     string // what the operator put in its place ("" when the action removed it)
	Reason    string // the operator's words, "" when the surface has no reason field
}

// Row is one stored ledger entry.
type Row struct {
	ID        int64  `json:"id"`
	Source    string `json:"source"`
	Ref       string `json:"ref"`
	ProjectID *int64 `json:"projectId"`
	Before    string `json:"before"`
	After     string `json:"after"`
	Reason    string `json:"reason"`
	NormKey   string `json:"normKey"`
	CreatedAt string `json:"createdAt"`
}

// Group is one correction identity (norm_key) folded over a window.
type Group struct {
	NormKey string   `json:"normKey"`
	Count   int      `json:"count"`
	Refs    []string `json:"refs"`    // distinct, newest first
	Sources []string `json:"sources"` // distinct, newest first
	Latest  string   `json:"latest"`  // created_at of the newest row
	Sample  string   `json:"sample"`  // the newest row's reason, or its after text
}

// NormKey is the identity a correction is counted under: the reason folded by
// wsingest.NormalizeLessonTitle, or the after text when there is no reason.
// "" means "no identity" and every reader skips it — two reason-less removals
// are never declared the same correction.
func NormKey(c Correction) string {
	if k := wsingest.NormalizeLessonTitle(c.Reason); k != "" {
		return k
	}
	return wsingest.NormalizeLessonTitle(c.After)
}

// Record appends one correction. A rejected or failed write is logged and
// returned; callers on the operator's request path discard it (see the
// package comment), tests assert on it.
func Record(db *sql.DB, c Correction, now time.Time) error {
	err := record(db, c, now)
	if err != nil {
		log.Printf("warning: corrections: %s %s not recorded: %v", c.Source, c.Ref, err)
	}
	return err
}

func record(db *sql.DB, c Correction, now time.Time) error {
	if db == nil {
		return fmt.Errorf("%w: nil db", ErrInvalid)
	}
	if !sources[c.Source] {
		return fmt.Errorf("%w: unknown source %q", ErrInvalid, c.Source)
	}
	if strings.TrimSpace(c.Ref) == "" {
		return fmt.Errorf("%w: empty ref", ErrInvalid)
	}
	var project any
	if c.ProjectID != nil {
		project = *c.ProjectID
	}
	_, err := db.Exec(`INSERT INTO operator_corrections
		(source, ref, project_id, before, after, reason, norm_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Source, strings.TrimSpace(c.Ref), project, c.Before, c.After, strings.TrimSpace(c.Reason),
		NormKey(c), now.UTC().Format(tsFmt))
	return err
}

// Groups folds the corrections made since `since` by norm_key, biggest group
// first (norm_key breaks ties so an unchanged window returns the same order).
// Rows with an empty norm_key are excluded — ” is "no identity", not one.
func Groups(db *sql.DB, since time.Time) ([]Group, error) {
	rows, err := db.Query(`
		SELECT norm_key, source, ref, reason, after, created_at
		  FROM operator_corrections
		 WHERE norm_key <> '' AND created_at >= ?
		 ORDER BY created_at DESC, id DESC`, since.UTC().Format(tsFmt))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Group{}
	index := map[string]int{}
	seenRef := map[string]bool{}
	seenSrc := map[string]bool{}
	for rows.Next() {
		var key, source, ref, reason, after, created string
		if err := rows.Scan(&key, &source, &ref, &reason, &after, &created); err != nil {
			return nil, err
		}
		i, known := index[key]
		if !known {
			sample := reason
			if sample == "" {
				sample = after
			}
			out = append(out, Group{NormKey: key, Latest: created, Sample: sample})
			i = len(out) - 1
			index[key] = i
		}
		out[i].Count++
		if k := key + "\x00" + ref; !seenRef[k] {
			seenRef[k] = true
			out[i].Refs = append(out[i].Refs, ref)
		}
		if k := key + "\x00" + source; !seenSrc[k] {
			seenSrc[k] = true
			out[i].Sources = append(out[i].Sources, source)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].NormKey < out[j].NormKey
	})
	return out, nil
}

// DefaultListLimit bounds List when the caller passes limit <= 0; MaxListLimit
// caps any caller.
const (
	DefaultListLimit = 200
	MaxListLimit     = 1000
)

// List returns the corrections made since `since`, newest first.
func List(db *sql.DB, since time.Time, limit int) ([]Row, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	rows, err := db.Query(`
		SELECT id, source, ref, project_id, before, after, reason, norm_key, created_at
		  FROM operator_corrections
		 WHERE created_at >= ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`, since.UTC().Format(tsFmt), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		var project sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Source, &r.Ref, &project, &r.Before, &r.After, &r.Reason,
			&r.NormKey, &r.CreatedAt); err != nil {
			return nil, err
		}
		if project.Valid {
			v := project.Int64
			r.ProjectID = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
