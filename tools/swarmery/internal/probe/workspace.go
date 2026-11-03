package probe

// The temp trees a probe runs in. Base is exported read-only from the repo's
// git objects (`git archive`, no fetch, no checkout, no worktree); variant is a
// copy of base with the proposal's diff applied. Both live under one
// os.MkdirTemp dir that is removed when the job ends — the operator's
// worktrees are never touched.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// Source materializes repo paths into dst, laid out like the repo root.
type Source interface {
	Export(ctx context.Context, dst string, paths []string) error
}

// GitSource exports paths at Ref from the git checkout at Repo with
// `git archive`, which only reads the object store.
type GitSource struct {
	Repo string
	Ref  string
}

// Export implements Source.
func (g GitSource) Export(ctx context.Context, dst string, paths []string) error {
	if g.Repo == "" {
		return errors.New("probe: no repo attached (the marketplace clone is missing)")
	}
	ref := g.Ref
	if ref == "" {
		ref = "origin/main"
	}
	args := append([]string{"archive", "--format=tar", ref, "--"}, paths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Repo
	// git archive only reads objects; never let it take index.lock in the repo.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git archive %s: %v (%s)", ref, err, strings.TrimSpace(stderr.String()))
	}
	return untar(&stdout, dst)
}

// untar extracts regular files and directories. Symlinks and anything whose
// name escapes dst are skipped: the tree only needs markdown, YAML and JS.
func untar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := path.Clean(h.Name)
		if name == "." || strings.HasPrefix(name, "../") || name == ".." || path.IsAbs(name) {
			continue
		}
		out := filepath.Join(dst, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(f, tr) //nolint:gosec // archive of our own repo, size bounded by it
			if err := f.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return cerr
			}
		}
	}
}

// workspace is one job's pair of trees.
type workspace struct {
	root, base, variant string
}

func (w workspace) cleanup() { _ = os.RemoveAll(w.root) }

// prepare builds base and variant for p. It fails — without writing any row —
// when the target drifted since generation (the diff would describe a file that
// no longer exists in that shape) or when the diff touches anything but the
// target (a diff that edits evals/ would grade itself).
func prepare(ctx context.Context, src Source, p Proposal) (workspace, error) {
	target := p.target()
	pack := strings.Split(target, "/")[1]
	root, err := os.MkdirTemp("", "swarmery-probe-*")
	if err != nil {
		return workspace{}, err
	}
	ws := workspace{root: root, base: filepath.Join(root, "base"), variant: filepath.Join(root, "variant")}
	fail := func(err error) (workspace, error) {
		ws.cleanup()
		return workspace{}, err
	}
	if err := os.MkdirAll(ws.base, 0o755); err != nil {
		return fail(err)
	}
	if err := src.Export(ctx, ws.base, []string{"evals", "plugins/" + pack}); err != nil {
		return fail(err)
	}
	cur, err := os.ReadFile(filepath.Join(ws.base, filepath.FromSlash(target)))
	if err != nil {
		return fail(fmt.Errorf("target %s missing from the base tree: %w", target, err))
	}
	if sum := sha256.Sum256(cur); hex.EncodeToString(sum[:]) != p.BaseSHA256 {
		return fail(fmt.Errorf("target %s changed since the proposal was generated (sha256 mismatch)", target))
	}
	if err := copyTree(ws.base, ws.variant); err != nil {
		return fail(err)
	}
	if err := applyDiff(ctx, root, ws.variant, p.Diff, target); err != nil {
		return fail(err)
	}
	return ws, nil
}

// applyDiff applies diff inside dir with `git apply` (plain-patch mode: dir is
// not a repository, and GIT_CEILING_DIRECTORIES keeps git from discovering one
// above it). Every path the diff touches must be target.
func applyDiff(ctx context.Context, scratch, dir, diff, target string) error {
	if strings.TrimSpace(diff) == "" {
		return errors.New("proposal has no diff to probe")
	}
	patch := filepath.Join(scratch, "proposal.patch")
	if err := os.WriteFile(patch, []byte(diff), 0o644); err != nil {
		return err
	}
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := git("apply", "--numstat", patch)
	if err != nil {
		return fmt.Errorf("git apply --numstat: %v (%s)", err, strings.TrimSpace(out))
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[2] != target {
			return fmt.Errorf("diff touches %q; a probe only patches %s", strings.TrimSpace(line), target)
		}
	}
	if out, err := git("apply", patch); err != nil {
		return fmt.Errorf("diff does not apply to the base tree: %v (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

// copyTree copies regular files and directories from src to dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
}
