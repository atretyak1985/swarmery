package lessons

// Promotion (15.4): graduate an ACTIVE lesson into the consumer repo's nested
// CLAUDE.md for its area — the file Claude Code loads when it works in that
// directory, so the lesson then reaches interactive sessions too, through the
// repo's own mechanism rather than a hook.
//
// The write is an ordinary file change for the operator to review: it is made
// on a NEW branch (swarm/lesson-L<id>) in a throwaway git worktree of the
// consumer repo, committed there, and the worktree is removed (the branch
// stays). The main checkout — its checked-out branch, index and working tree —
// is never touched. One bullet per lesson, id kept: "- [L-12] guidance". The
// file is created when absent. Every attempt, failed or not, is logged in
// lesson_promotions.
//
// Cross-project lessons follow the repository's graduation rule instead: they
// are proposed for a pack or core by hand, de-flavored, and pass the neutrality
// scan there. This action only ever writes into the lesson's own project.

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// PromoteInput is the operator's request body.
type PromoteInput struct {
	// Area overrides the directory: "" ⇒ the lesson's first area glob.
	Area string `json:"area"`
}

// Promotion is one lesson_promotions row.
type Promotion struct {
	ID        int64  `json:"id"`
	LessonID  int64  `json:"lessonId"`
	RepoRoot  string `json:"repoRoot"`
	AreaDir   string `json:"areaDir"`
	FilePath  string `json:"filePath"`
	Branch    string `json:"branch"`
	CommitSHA string `json:"commitSha"`
	Error     string `json:"error"`
	CreatedAt string `json:"createdAt"`
}

// RepoResolver maps a project path and the phase's declared Repo cell to the
// checkout to write in (production: repopath.Resolve).
type RepoResolver func(projectPath string, cells ...string) (string, error)

// PromoteBranch is the branch a lesson is promoted on.
func PromoteBranch(id int64) string { return fmt.Sprintf("swarm/lesson-L%d", id) }

// AreaDir turns an area glob into the repo-relative directory whose CLAUDE.md
// receives the lesson: the glob cut at its first wildcard segment (the phase-13
// normalization), a trailing file name dropped. "." is the repo root. An entry
// that climbs out of the repo is invalid.
func AreaDir(area string) (string, error) {
	n := surprise.NormArea(area)
	if n == "" {
		n = "."
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: area %q leaves the repository", ErrInvalid, area)
		}
	}
	if last := path.Base(n); n != "." && path.Ext(last) != "" && !strings.HasPrefix(last, ".") {
		n = path.Dir(n)
	}
	return n, nil
}

// Bullet is the line a promoted lesson becomes.
func Bullet(l Lesson) string {
	return "- [" + Ref(l.ID) + "] " + strings.Join(strings.Fields(l.Guidance), " ")
}

// Promote writes an active lesson into <repo>/<area>/CLAUDE.md on a new branch
// and returns the lesson (PromotedBranch set). ErrState: not active, already on
// that branch, or already in the file; ErrInvalid: an unusable area.
func Promote(db *sql.DB, git worktree.Git, resolve RepoResolver, id int64, in PromoteInput, now time.Time) (Lesson, error) {
	l, err := Get(db, id)
	if err != nil {
		return l, err
	}
	if l.Status != StatusActive {
		return l, fmt.Errorf("%w: only an active lesson can be promoted (this one is %s)", ErrState, l.Status)
	}
	area := strings.TrimSpace(in.Area)
	if area == "" && len(l.AreaGlobs) > 0 {
		area = l.AreaGlobs[0]
	}
	dir, err := AreaDir(area)
	if err != nil {
		return l, err
	}
	var projectPath, repoCell string
	if err := db.QueryRow(`SELECT p.path, COALESCE(e.repo, '') FROM epic_phases e
		JOIN tasks t ON t.id = e.workspace_task_id JOIN projects p ON p.id = t.project_id
		WHERE e.id = ?`, l.PhaseID).Scan(&projectPath, &repoCell); err != nil {
		return l, fmt.Errorf("%w: the lesson's phase no longer maps to a project: %v", ErrState, err)
	}
	root, err := resolve(projectPath, repoCell)
	if err != nil {
		return l, fmt.Errorf("resolve the project's repository: %w", err)
	}
	rel := path.Join(dir, "CLAUDE.md")
	p := Promotion{LessonID: id, RepoRoot: root, AreaDir: dir, FilePath: rel, Branch: PromoteBranch(id), CreatedAt: stamp(now)}
	p.CommitSHA, err = writeOnBranch(git, root, p.Branch, dir, rel, l)
	if err != nil {
		p.Error = err.Error()
	}
	logPromotion(db, p)
	if err != nil {
		return l, err
	}
	log.Printf("lessons: promoted %s into %s:%s on branch %s (%s)", Ref(id), root, rel, p.Branch, p.CommitSHA)
	return Get(db, id)
}

func logPromotion(db *sql.DB, p Promotion) {
	if _, err := db.Exec(`INSERT INTO lesson_promotions
		(lesson_id, repo_root, area_dir, file_path, branch, commit_sha, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.LessonID, p.RepoRoot, p.AreaDir, p.FilePath, p.Branch, p.CommitSHA, p.Error, p.CreatedAt); err != nil {
		log.Printf("warning: lessons: log promotion of %s: %v", Ref(p.LessonID), err)
	}
}

// writeOnBranch cuts branch from HEAD in a throwaway worktree, appends the
// bullet to rel, commits, and removes the worktree (the branch stays).
func writeOnBranch(git worktree.Git, root, branch, dir, rel string, l Lesson) (sha string, err error) {
	if _, err := git.Run(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return "", fmt.Errorf("%w: branch %s already exists — review or delete it first", ErrState, branch)
	}
	tmp, err := os.MkdirTemp("", "swarmery-lesson-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	wt := filepath.Join(tmp, "wt")
	if _, err := git.Run(root, "worktree", "add", "-b", branch, wt, "HEAD"); err != nil {
		return "", fmt.Errorf("create the promotion worktree: %w", err)
	}
	committed := false
	defer func() {
		if _, rmErr := git.Run(root, "worktree", "remove", "--force", wt); rmErr != nil {
			log.Printf("warning: lessons: remove promotion worktree %s: %v", wt, rmErr)
		}
		if !committed {
			_, _ = git.Run(root, "branch", "-D", branch)
		}
	}()

	if st, err := os.Stat(filepath.Join(wt, filepath.FromSlash(dir))); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%w: area directory %q does not exist in %s", ErrInvalid, dir, root)
	}
	file := filepath.Join(wt, filepath.FromSlash(rel))
	body, err := os.ReadFile(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		body = []byte("# Lessons for `" + dir + "`\n\n")
	case err != nil:
		return "", err
	}
	text := string(body)
	if strings.Contains(text, "["+Ref(l.ID)+"]") {
		return "", fmt.Errorf("%w: %s already carries %s", ErrState, rel, Ref(l.ID))
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += Bullet(l) + "\n"
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		return "", err
	}
	if _, err := git.Run(wt, "add", "--", rel); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("docs(lessons): promote %s into %s\n\n%s", Ref(l.ID), rel, l.Title)
	if _, err := git.Run(wt, "commit", "-m", msg); err != nil {
		return "", err
	}
	out, err := git.Run(wt, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	committed = true
	return strings.TrimSpace(out), nil
}
