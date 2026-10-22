package worktree

// The plan document an executor works against lives in the private workspace,
// OUTSIDE the repository. The execution contract used to hand the agent that
// absolute path and tell it to "edit it in place (it is outside the repo)" —
// two lines after telling it "Work ONLY here".
//
// That contradiction is not a wording problem. An agent isolated to a worktree
// is REFUSED when it reaches outside it, and one retro window measured the
// result: 56 isolation errors, 4 plan-read refusals, 3 summaries written to
// scratch files the dashboard never reads, and an 81% error rate for the
// executor agent. The contract asked for something the sandbox forbids.
//
// So the document is lent INTO the worktree, exactly the way
// syncUntrackedConfig lends the project's config files, and for the same
// underlying reason: `git worktree add` only materializes committed files, and
// this file is not in git at all. The agent then has one root and everything it
// needs inside it.
//
// The difference from a config file is the return trip. The dashboard renders
// the WORKSPACE copy's `## Completion Report` and nothing else, so a report
// written to the lent copy has to come back or the operator sees "no summary of
// the work written" over work that shipped — the exact failure already recorded
// three times. ReturnPlanDoc is therefore not an optimisation; it is the half
// that makes lending safe.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

// PlanDocDir is where a lent document lands, relative to the worktree root.
// Dot-prefixed so it never collides with the project's own tree, and fixed so
// the contract can name it without interpolating anything.
const PlanDocDir = ".swarmery/plan"

// ReportPath is where a card WITHOUT a plan document writes its Completion
// Report, relative to the worktree root. Same dot-prefixed directory as a lent
// doc, and fixed for the same reason: the contract names it without
// interpolating anything.
//
// A docless card used to be told nothing about where its report should go, so
// its report landed in the reply — which the dashboard does not read. The
// destination has to exist for the instruction to be honest, which is what
// CollectReport is for.
const ReportPath = ".swarmery/report.md"

// maxReportBytes caps what CollectReport hands back. A Completion Report is a
// summary; anything past this is a log the agent pasted, and it would land in a
// board field that renders inline.
const maxReportBytes = 8192

// maxPlanDocBytes caps what ReturnPlanDoc copies back into the workspace. A
// phase doc is tens of kilobytes; anything near this is not a plan document.
const maxPlanDocBytes = 4 << 20

// CollectReport reads the worktree's ReportPath and returns its trimmed
// contents, or "" when the agent wrote nothing. Missing file, unreadable file
// and empty file are all "" with no error: this is the docless counterpart of
// ReturnPlanDoc, and like it, a run whose report never arrived is a run that
// still shipped its commits.
func CollectReport(worktreePath string) string {
	if worktreePath == "" {
		return ""
	}
	// The worktree is the repository's tree: a report path that resolves through
	// a symlink (or is not a plain file) is refused, never followed out of it.
	data, err := func() ([]byte, error) {
		if err := checkNoSymlink(worktreePath, ReportPath); err != nil {
			return nil, err
		}
		return readRegular(filepath.Join(worktreePath, ReportPath), maxReportBytes)
	}()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("warning: worktree: report.md refused: %v", err)
		}
		return ""
	}
	if len(data) > maxReportBytes {
		data = append(data[:maxReportBytes], []byte("\n… (truncated)")...)
	}
	return string(bytes.TrimSpace(data))
}

// LendPlanDoc copies docPath into the worktree at PlanDocDir/<basename> and
// returns that path RELATIVE to the worktree root — the form the execution
// contract quotes, so the agent never sees an absolute path outside its root.
//
// An empty docPath returns "" with no error: a card without a plan document is
// the normal case, not a failure.
func LendPlanDoc(worktreePath, docPath string) (string, error) {
	if worktreePath == "" || docPath == "" {
		return "", nil
	}
	data, err := os.ReadFile(docPath)
	if err != nil {
		return "", fmt.Errorf("read plan doc %s: %w", docPath, err)
	}
	rel := LentPlanDocRel(docPath)
	dst := filepath.Join(worktreePath, rel)
	// Refuse a symlink anywhere on the way BEFORE MkdirAll (which would follow a
	// symlinked .swarmery and create directories at its target), again after it,
	// and once more at the open itself (O_NOFOLLOW): a link planted at dst is an
	// error, never a file to write through.
	if err := checkNoSymlink(worktreePath, rel); err != nil {
		return "", fmt.Errorf("lend plan doc into worktree: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("create %s in worktree: %w", PlanDocDir, err)
	}
	if err := checkNoSymlink(worktreePath, rel); err != nil {
		return "", fmt.Errorf("lend plan doc into worktree: %w", err)
	}
	f, err := openNoFollowWrite(dst, 0o644)
	if err != nil {
		return "", fmt.Errorf("write plan doc into worktree: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", fmt.Errorf("write plan doc into worktree: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write plan doc into worktree: %w", err)
	}
	return rel, nil
}

// LentPlanDocRel is where LendPlanDoc puts docPath, relative to the worktree
// root. It is exported for the callers that must find a lent copy WITHOUT the
// value LendPlanDoc returned — a run adopted after a daemon restart, whose
// in-memory docRel died with the previous process.
func LentPlanDocRel(docPath string) string {
	return filepath.Join(PlanDocDir, filepath.Base(docPath))
}

// ReturnPlanDoc copies the worktree's copy back over the workspace document
// when the agent actually changed it, and reports whether it wrote.
//
// Called on EVERY exit path — done, blocked, failed, cancelled — because the
// contract asks for a Completion Report on the blocked path too, and a report
// about why the work stopped is the one most worth not losing.
//
// It compares before writing. Copying back an untouched file would rewrite the
// workspace document's mtime on every run and make "was this phase touched?"
// unanswerable from the filesystem; worse, a run that never started would
// silently restamp a doc a human had edited in the meantime.
func ReturnPlanDoc(worktreePath, relPath, docPath string) (bool, error) {
	if worktreePath == "" || relPath == "" || docPath == "" {
		return false, nil
	}
	src := filepath.Join(worktreePath, relPath)
	// The workspace document is pushed to a remote: a lent copy that resolves
	// through a symlink, or is not a plain file, must never hand it the bytes of
	// a file outside the worktree.
	if err := checkNoSymlink(worktreePath, relPath); err != nil {
		return false, fmt.Errorf("lent plan doc %s refused: %w", src, err)
	}
	updated, err := readRegular(src, maxPlanDocBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The agent deleted it, or the worktree was already torn down.
			// Nothing to return, and nothing to complain about.
			return false, nil
		}
		return false, fmt.Errorf("read lent plan doc %s: %w", src, err)
	}
	if len(updated) > maxPlanDocBytes {
		return false, fmt.Errorf("lent plan doc %s exceeds %d bytes; refusing to copy it back", src, maxPlanDocBytes)
	}
	current, err := os.ReadFile(docPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read workspace plan doc %s: %w", docPath, err)
	}
	if bytes.Equal(current, updated) {
		return false, nil
	}
	if len(bytes.TrimSpace(updated)) == 0 {
		// An empty copy is damage, not an edit. Never let it overwrite the
		// workspace document.
		return false, fmt.Errorf("lent plan doc %s came back empty; refusing to overwrite %s", src, docPath)
	}
	if err := os.WriteFile(docPath, updated, 0o644); err != nil {
		return false, fmt.Errorf("write plan doc back to %s: %w", docPath, err)
	}
	return true, nil
}

// ReturnPlanDocLogged is ReturnPlanDoc with the house logging, so every caller
// records which branch it took rather than each inventing its own phrasing.
// Best-effort by construction: a failed return is logged, never fatal — the
// work itself is already committed in the worktree's branch.
func ReturnPlanDocLogged(what, worktreePath, relPath, docPath string) {
	if relPath == "" || docPath == "" {
		return
	}
	wrote, err := ReturnPlanDoc(worktreePath, relPath, docPath)
	switch {
	case err != nil:
		log.Printf("warning: worktree: %s: plan doc not returned to %s: %v", what, docPath, err)
	case wrote:
		log.Printf("worktree: %s: returned the edited plan doc to %s", what, docPath)
	default:
		log.Printf("worktree: %s: plan doc unchanged in the worktree, %s left alone", what, docPath)
	}
}
