// Package credstore is the daemon-owned store for the human's VCS token (D3 of
// the landing plan): one file per host, <claudeacct.SecretsDir()>/vcs-<host>.env,
// mode 0600, read back with the same refusal rule claudeacct applies to its
// secret stores — a file with any group/other permission bit is not read.
//
// Every `gh`/`glab` call the daemon makes runs with Env(host). Once a token is
// stored, that is the token plus an ISOLATED config dir (GH_CONFIG_DIR /
// GLAB_CONFIG_DIR under SecretsDir), so the daemon neither reads nor rewrites
// the operator's own CLI config and does not depend on which account the
// operator's shell is logged in as. Until then Env is nil and the CLI runs on
// the operator's own login, exactly as board land always has.
//
// # Import direction
//
// credstore is the LEAF of internal/repoprovider: it imports only the standard
// library and internal/claudeacct. The parent package imports credstore (for
// Redact, which every error leaving repoprovider passes through), so credstore
// must never import repoprovider. That is why ImportFromCLI takes the minimal
// Runner interface defined here (repoprovider.Exec satisfies it structurally)
// and a provider kind as a plain string.
//
// # Redaction
//
// Redact masks the token shapes GitHub and GitLab issue (ghp_/gho_/ghu_/ghs_/
// ghr_, github_pat_, glpat-) AND every token literal this process has loaded or
// written, so a token in an unexpected shape is still masked once the daemon
// has seen it.
package credstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

const (
	// GitHubTokenKey / GitLabTokenKey are the env names gh and glab read a
	// token from. They are also the only keys a store file carries.
	GitHubTokenKey = "GH_TOKEN"
	GitLabTokenKey = "GITLAB_TOKEN"

	// ghConfigDirName / glabConfigDirName are the isolated CLI config dirs,
	// relative to SecretsDir.
	ghConfigDirName   = "vcs-gh-config"
	glabConfigDirName = "vcs-glab-config"

	fileMode   = 0o600
	dirMode    = 0o700
	groupOther = 0o077

	// minLiteral is the shortest loaded token Redact masks by value. Shorter
	// strings are not plausible tokens, and masking them would shred ordinary
	// text in an error detail.
	minLiteral = 8
	// maxStoreBytes bounds a store file read: it holds one or two lines.
	maxStoreBytes = 64 << 10
)

// Kind values ImportFromCLI accepts (the string form of repoprovider.Kind).
const (
	KindGitHub = "github"
	KindGitLab = "gitlab"
)

// ErrInsecure is returned when a store file (or its directory) is open beyond
// its owner, is a symlink, or is not a regular file. The file is not read.
var ErrInsecure = errors.New("credstore: store file refused")

// ErrNoStoreDir is returned when no secrets directory can be resolved.
var ErrNoStoreDir = errors.New("credstore: no secrets directory")

// Runner is the subset of repoprovider.Exec that ImportFromCLI needs. Defined
// here, not imported, to keep this package a leaf (see the package doc).
type Runner interface {
	Run(ctx context.Context, dir string, env []string, name string, args ...string) (stdout, stderr string, err error)
}

// Path is the store file for host, or "" when there is no secrets dir or the
// host sanitizes to nothing.
func Path(host string) string {
	base := claudeacct.SecretsDir()
	h := sanitize(host)
	if base == "" || h == "" {
		return ""
	}
	return filepath.Join(base, "vcs-"+h+".env")
}

// sanitize maps a host (possibly with a port) to a bare file-name fragment:
// lowercase, [a-z0-9.-] kept, everything else (":" of a port included) "_".
// A leading "." is dropped so the result can never be "." / ".." or hidden.
func sanitize(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.TrimLeft(b.String(), ".")
}

// Write stores token under key for host, merging with any keys the (valid)
// existing file already carries. The file is written 0600 through an exclusive
// temp file in the same directory and renamed into place, so a reader sees the
// old file or the new one, never half of one. The secrets dir is created 0700.
func Write(host, key, token string) error {
	path := Path(host)
	if path == "" {
		return ErrNoStoreDir
	}
	if !validKey(key) {
		return fmt.Errorf("credstore: invalid key %q", key)
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("credstore: empty or multi-line token")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("credstore: create secrets dir: %w", err)
	}
	// MkdirAll leaves an EXISTING dir's mode alone. A store written into a
	// dir open to group/other is one Load refuses, so Write refuses it too —
	// rather than report a success the next read contradicts. The dir is not
	// chmodded behind the operator's back: it is shared with claudeacct.
	if info, err := os.Stat(dir); err != nil {
		return fmt.Errorf("credstore: inspect secrets dir: %w", err)
	} else if perm := info.Mode().Perm(); perm&groupOther != 0 {
		return fmt.Errorf("%w: directory %s has mode %04o (want %04o); fix it with: chmod %04o %s",
			ErrInsecure, dir, perm, dirMode, dirMode, dir)
	}
	vals, err := load(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		// A refused file is replaced, not merged: nothing in it is trusted.
		vals = nil
	}
	if vals == nil {
		vals = map[string]string{}
	}
	vals[key] = token
	remember(token)
	return writeAtomic(path, render(vals))
}

func render(vals map[string]string) []byte {
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(vals[k])
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// writeAtomic: exclusive temp (os.CreateTemp is O_EXCL, 0600), chmod, fsync,
// close, rename — the claudeacct/binding.go writeAtomic pattern.
func writeAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("credstore: write %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("credstore: write %s: %w", path, err)
	}
	if err = tmp.Chmod(fileMode); err != nil {
		return fmt.Errorf("credstore: write %s: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("credstore: write %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("credstore: write %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("credstore: write %s: %w", path, err)
	}
	return nil
}

// Load reads host's store: its KEY=value pairs, or fs.ErrNotExist when there is
// no store, or ErrInsecure when the file is refused. Loaded token values are
// remembered for Redact.
func Load(host string) (map[string]string, error) {
	path := Path(host)
	if path == "" {
		return nil, ErrNoStoreDir
	}
	return load(path)
}

// Has reports whether host has a store file the loader accepts and that
// carries at least one token.
func Has(host string) bool {
	vals, err := Load(host)
	return err == nil && len(vals) > 0
}

func load(path string) (map[string]string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s is a symlink", ErrInsecure, path)
		}
		return nil, err
	}
	defer f.Close()
	if dinfo, err := os.Stat(filepath.Dir(path)); err != nil {
		return nil, err
	} else if dinfo.Mode().Perm()&groupOther != 0 {
		return nil, fmt.Errorf("%w: directory %s has mode %04o (want %04o)",
			ErrInsecure, filepath.Dir(path), dinfo.Mode().Perm(), dirMode)
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrInsecure, path)
	}
	if perm := info.Mode().Perm(); perm&groupOther != 0 {
		return nil, fmt.Errorf("%w: %s has mode %04o (want %04o); fix it with: chmod %04o %s",
			ErrInsecure, path, perm, fileMode, fileMode, path)
	}
	vals := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), maxStoreBytes)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !validKey(k) || v == "" {
			continue // malformed: skipped, never logged (it may be a mangled token)
		}
		vals[k] = v
		remember(v)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("credstore: read %s: %w", path, err)
	}
	return vals, nil
}

func validKey(k string) bool { return k == GitHubTokenKey || k == GitLabTokenKey }

// GitHubEnterpriseTokenKey is the env name gh reads a token from for any host
// other than github.com and *.ghe.com (GitHub Enterprise Server). gh ignores
// GH_TOKEN for such a host.
const GitHubEnterpriseTokenKey = "GH_ENTERPRISE_TOKEN"

// Env is the env DELTA every gh/glab call for host runs with, when the daemon
// holds a token for host: the store's token(s), plus GH_CONFIG_DIR and
// GLAB_CONFIG_DIR pointing at isolated dirs under SecretsDir (created 0700).
// A GitHub token for a GitHub Enterprise Server host is also exported as
// GH_ENTERPRISE_TOKEN, the only variable gh reads for such a host.
//
// nil — "run the CLI exactly as the operator's own login would" — when no
// secrets dir resolves, when host has no store, and when the store is refused
// or carries no token. An operator logged in only through `gh auth login`
// therefore keeps working unchanged until a token is imported. A refused store
// is logged by path and mode only — never a value.
func Env(host string) []string {
	base := claudeacct.SecretsDir()
	if base == "" {
		return nil
	}
	vals, err := Load(host)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Print(err) // path + mode only; ErrInsecure never carries a value
		}
		return nil
	}
	var out []string
	if v, ok := vals[GitHubTokenKey]; ok {
		out = append(out, GitHubTokenKey+"="+v)
		if isEnterpriseHost(host) {
			out = append(out, GitHubEnterpriseTokenKey+"="+v)
		}
	}
	if v, ok := vals[GitLabTokenKey]; ok {
		out = append(out, GitLabTokenKey+"="+v)
	}
	if len(out) == 0 {
		return nil
	}
	for _, kv := range [][2]string{
		{"GH_CONFIG_DIR", filepath.Join(base, ghConfigDirName)},
		{"GLAB_CONFIG_DIR", filepath.Join(base, glabConfigDirName)},
	} {
		if err := os.MkdirAll(kv[1], dirMode); err != nil {
			log.Printf("credstore: create %s: %v", kv[1], err)
		}
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

// isEnterpriseHost reports whether gh treats host as GitHub Enterprise Server,
// i.e. reads GH_ENTERPRISE_TOKEN rather than GH_TOKEN for it: anything but
// github.com and the *.ghe.com (GHE.com data residency) hosts. A port is
// ignored.
func isEnterpriseHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h != "" && h != "github.com" && !strings.HasSuffix(h, ".ghe.com")
}

// glabTokenLine matches the token line `glab auth status --show-token` prints
// (on stderr in current glab versions): "✓ Token: glpat-…".
var glabTokenLine = regexp.MustCompile(`(?m)Token:\s*(\S+)`)

// ImportFromCLI copies the token the operator's own CLI login holds into the
// daemon's store: `gh auth token --hostname <host>` for GitHub, `glab auth
// status --hostname <host> --show-token` for GitLab. The CLI runs with a nil
// env delta — i.e. against the operator's own config, which is the point. No
// token text ever appears in a returned error.
func ImportFromCLI(ctx context.Context, run Runner, kind, host string) error {
	var (
		key, tok string
	)
	switch kind {
	case KindGitHub:
		stdout, stderr, err := run.Run(ctx, "", nil, "gh", "auth", "token", "--hostname", host)
		if err != nil {
			return fmt.Errorf("credstore: gh auth token: %s", RedactedTail(stderr, err, tailBytes))
		}
		key, tok = GitHubTokenKey, strings.TrimSpace(stdout)
	case KindGitLab:
		stdout, stderr, err := run.Run(ctx, "", nil, "glab", "auth", "status", "--hostname", host, "--show-token")
		if err != nil {
			// --show-token prints the token even on a failing status; mask the
			// Token: line whatever shape the token has, then redact.
			masked := glabTokenLine.ReplaceAllString(stderr, "Token: ***")
			return fmt.Errorf("credstore: glab auth status: %s", RedactedTail(masked, err, tailBytes))
		}
		m := glabTokenLine.FindStringSubmatch(stdout + "\n" + stderr)
		if m == nil {
			return errors.New("credstore: glab auth status printed no token")
		}
		key, tok = GitLabTokenKey, m[1]
	default:
		return fmt.Errorf("credstore: cannot import a token for provider %q", kind)
	}
	if tok == "" {
		return fmt.Errorf("credstore: %s printed no token for %s", kind, host)
	}
	return Write(host, key, tok)
}

// tailBytes bounds the tool output a credstore error carries.
const tailBytes = 2048

// RedactedTail is the most informative text of a failed call — stderr when it
// said something, the process error otherwise — REDACTED FIRST and only then
// cut to its last max bytes. The order is the point: cutting first can split a
// token at the boundary into a fragment no token pattern matches any more.
func RedactedTail(stderr string, err error, max int) string {
	s := strings.TrimSpace(stderr)
	if s == "" && err != nil {
		s = err.Error()
	}
	s = Redact(s)
	if max > 0 && len(s) > max {
		s = s[len(s)-max:]
	}
	return s
}

// tokenShapes are the token formats GitHub and GitLab issue.
var tokenShapes = regexp.MustCompile(
	`gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}`)

var (
	literalsMu sync.RWMutex
	literals   = map[string]struct{}{}
)

// remember registers a token literal for Redact.
func remember(tok string) {
	if len(tok) < minLiteral {
		return
	}
	literalsMu.Lock()
	literals[tok] = struct{}{}
	literalsMu.Unlock()
}

// Redact masks every known token shape and every token literal this process
// has loaded or written with "***".
func Redact(s string) string {
	if s == "" {
		return s
	}
	literalsMu.RLock()
	lits := make([]string, 0, len(literals))
	for lit := range literals {
		lits = append(lits, lit)
	}
	literalsMu.RUnlock()
	// Longest first, so a literal that contains another is masked whole.
	sort.Slice(lits, func(i, j int) bool { return len(lits[i]) > len(lits[j]) })
	for _, lit := range lits {
		s = strings.ReplaceAll(s, lit, "***")
	}
	return tokenShapes.ReplaceAllString(s, "***")
}
