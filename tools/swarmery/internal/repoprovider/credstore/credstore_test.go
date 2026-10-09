package credstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points SecretsDir at a fresh temp dir: no test touches ~/.swarmery.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// t.TempDir is 0755 on macOS; a real secrets dir is 0700, and the loader
	// refuses anything more open.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", dir)
	return dir
}

type fakeRunner struct {
	stdout, stderr string
	err            error
	calls          []string
	envs           [][]string
}

func (f *fakeRunner) Run(_ context.Context, _ string, env []string, name string, args ...string) (string, string, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	f.envs = append(f.envs, env)
	return f.stdout, f.stderr, f.err
}

func TestPathSanitizesHost(t *testing.T) {
	dir := isolate(t)
	cases := map[string]string{
		"github.com":         "vcs-github.com.env",
		"GitLab.Example.COM": "vcs-gitlab.example.com.env",
		"git.corp:8443":      "vcs-git.corp_8443.env",
		"../../etc/passwd":   "vcs-_.._etc_passwd.env",
		"  github.com  ":     "vcs-github.com.env",
	}
	for host, want := range cases {
		if got := Path(host); got != filepath.Join(dir, want) {
			t.Errorf("Path(%q) = %q, want %q", host, got, filepath.Join(dir, want))
		}
	}
	if got := Path(""); got != "" {
		t.Errorf("Path(\"\") = %q, want empty", got)
	}
	if got := Path("..."); got != "" {
		t.Errorf("Path(\"...\") = %q, want empty", got)
	}
}

func TestWriteMode0600AndRoundTrip(t *testing.T) {
	dir := isolate(t)
	if err := Write("github.com", GitHubTokenKey, "ghp_abcdefghijklmnopqrstuvwxyz0123"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "vcs-github.com.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}
	vals, err := Load("github.com")
	if err != nil {
		t.Fatal(err)
	}
	if vals[GitHubTokenKey] != "ghp_abcdefghijklmnopqrstuvwxyz0123" {
		t.Fatalf("round trip lost the token: %v", vals)
	}
	if !Has("github.com") {
		t.Fatal("Has = false after Write")
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
}

func TestWriteMergesKeysAndRejectsBadInput(t *testing.T) {
	isolate(t)
	if err := Write("git.corp", GitHubTokenKey, "first-token-value"); err != nil {
		t.Fatal(err)
	}
	if err := Write("git.corp", GitLabTokenKey, "second-token-value"); err != nil {
		t.Fatal(err)
	}
	vals, err := Load("git.corp")
	if err != nil {
		t.Fatal(err)
	}
	if vals[GitHubTokenKey] != "first-token-value" || vals[GitLabTokenKey] != "second-token-value" {
		t.Fatalf("merge lost a key: %v", vals)
	}
	if err := Write("git.corp", "AWS_SECRET", "x-token-value"); err == nil {
		t.Fatal("Write accepted an unknown key")
	}
	if err := Write("git.corp", GitHubTokenKey, "  "); err == nil {
		t.Fatal("Write accepted an empty token")
	}
	if err := Write("git.corp", GitHubTokenKey, "a\nb"); err == nil {
		t.Fatal("Write accepted a multi-line token")
	}
	if err := Write("", GitHubTokenKey, "tok-tok-tok"); !errors.Is(err, ErrNoStoreDir) {
		t.Fatalf("Write with empty host = %v, want ErrNoStoreDir", err)
	}
}

// Review fix 4: Write into an existing secrets dir open to group/other fails
// with ErrInsecure instead of "succeeding" into a store Load then refuses.
func TestWriteRefusesOpenSecretsDir(t *testing.T) {
	dir := isolate(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err := Write("github.com", GitHubTokenKey, "never-written-token")
	if !errors.Is(err, ErrInsecure) {
		t.Fatalf("Write into 0755 dir = %v, want ErrInsecure", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "vcs-github.com.env")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("store file was written anyway: %v", statErr)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o755 {
		t.Fatalf("Write changed the dir mode to %04o", info.Mode().Perm())
	}
	// A dir Write creates itself is 0700 and accepted.
	fresh := filepath.Join(t.TempDir(), "secrets")
	if err := os.Chmod(filepath.Dir(fresh), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", fresh)
	if err := Write("github.com", GitHubTokenKey, "fresh-dir-token"); err != nil {
		t.Fatalf("Write into a fresh dir: %v", err)
	}
	if info, _ := os.Stat(fresh); info.Mode().Perm() != 0o700 {
		t.Fatalf("created dir mode %04o", info.Mode().Perm())
	}
}

func TestWriteReplacesRefusedFile(t *testing.T) {
	dir := isolate(t)
	p := filepath.Join(dir, "vcs-github.com.env")
	if err := os.WriteFile(p, []byte("GITLAB_TOKEN=stale-token-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write("github.com", GitHubTokenKey, "fresh-token-value"); err != nil {
		t.Fatal(err)
	}
	vals, err := Load("github.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := vals[GitLabTokenKey]; ok {
		t.Fatal("Write merged keys from a refused (0644) file")
	}
}

func TestLoadRefuses0644(t *testing.T) {
	dir := isolate(t)
	p := filepath.Join(dir, "vcs-github.com.env")
	if err := os.WriteFile(p, []byte("GH_TOKEN=leaky-token-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil { // umask-proof
		t.Fatal(err)
	}
	if _, err := Load("github.com"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Load(0644) = %v, want ErrInsecure", err)
	}
	if Has("github.com") {
		t.Fatal("Has = true for a refused store")
	}
	for _, kv := range Env("github.com") {
		if strings.HasPrefix(kv, GitHubTokenKey+"=") {
			t.Fatalf("Env leaked a token from a 0644 store: %q", kv)
		}
	}
}

func TestLoadRefusesSymlinkAndOpenDir(t *testing.T) {
	dir := isolate(t)
	real := filepath.Join(t.TempDir(), "real.env")
	if err := os.WriteFile(real, []byte("GH_TOKEN=linked-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "vcs-github.com.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("github.com"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Load(symlink) = %v, want ErrInsecure", err)
	}

	// A store directory open to group/other is refused too.
	if err := os.WriteFile(filepath.Join(dir, "vcs-gitlab.com.env"), []byte("GITLAB_TOKEN=x-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if _, err := Load("gitlab.com"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Load(open dir) = %v, want ErrInsecure", err)
	}
}

func TestLoadMissingAndMalformed(t *testing.T) {
	dir := isolate(t)
	if _, err := Load("github.com"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load(missing) = %v, want ErrNotExist", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "vcs-dir.example.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("dir.example"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Load(directory) = %v, want ErrInsecure", err)
	}
	body := "# comment\n\nexport GH_TOKEN=exported-token-value\nnot a pair\nOTHER=x\nGITLAB_TOKEN=\n"
	if err := os.WriteFile(filepath.Join(dir, "vcs-mixed.example.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	vals, err := Load("mixed.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 || vals[GitHubTokenKey] != "exported-token-value" {
		t.Fatalf("Load(mixed) = %v", vals)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", "")
	t.Setenv("HOME", "")
	if _, err := Load(""); !errors.Is(err, ErrNoStoreDir) {
		t.Fatalf("Load(\"\") = %v, want ErrNoStoreDir", err)
	}
}

func TestEnvCarriesTokenAndIsolatedConfigDirs(t *testing.T) {
	dir := isolate(t)
	if err := Write("github.com", GitHubTokenKey, "ghp_envtokenenvtokenenvtokenenv00"); err != nil {
		t.Fatal(err)
	}
	env := Env("github.com")
	want := []string{
		"GH_TOKEN=ghp_envtokenenvtokenenvtokenenv00",
		"GH_CONFIG_DIR=" + filepath.Join(dir, "vcs-gh-config"),
		"GLAB_CONFIG_DIR=" + filepath.Join(dir, "vcs-glab-config"),
	}
	if strings.Join(env, "|") != strings.Join(want, "|") {
		t.Fatalf("Env = %v\nwant  %v", env, want)
	}
	for _, d := range []string{"vcs-gh-config", "vcs-glab-config"} {
		info, err := os.Stat(filepath.Join(dir, d))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode = %04o, want 0700", d, info.Mode().Perm())
		}
	}
	// No store: nil — the CLI runs on the operator's own login.
	if env := Env("gitlab.com"); env != nil {
		t.Fatalf("Env(no store) = %v, want nil", env)
	}
	// A store with no usable token is nil too.
	if err := os.WriteFile(filepath.Join(dir, "vcs-empty.example.env"), []byte("# nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if env := Env("empty.example"); env != nil {
		t.Fatalf("Env(empty store) = %v, want nil", env)
	}
}

// gh reads GH_TOKEN only for github.com and *.ghe.com; any other host is
// GitHub Enterprise Server and needs GH_ENTERPRISE_TOKEN.
func TestEnvEnterpriseToken(t *testing.T) {
	isolate(t)
	cases := map[string]bool{
		"github.com":        false,
		"acme.ghe.com":      false,
		"ghe.corp":          true,
		"GHE.Corp:8443":     true,
		"git.example.local": true,
	}
	for host, wantEnterprise := range cases {
		tok := "tok-" + sanitize(host) + "-value"
		if err := Write(host, GitHubTokenKey, tok); err != nil {
			t.Fatal(err)
		}
		env := strings.Join(Env(host), "\n")
		if !strings.Contains(env, "GH_TOKEN="+tok) {
			t.Errorf("%s: GH_TOKEN missing: %q", host, env)
		}
		if got := strings.Contains(env, "GH_ENTERPRISE_TOKEN="+tok); got != wantEnterprise {
			t.Errorf("%s: GH_ENTERPRISE_TOKEN present = %v, want %v", host, got, wantEnterprise)
		}
	}
	// A GitLab-only store never gets a GitHub enterprise token.
	if err := Write("gitlab.corp", GitLabTokenKey, "glab-only-token-value"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(Env("gitlab.corp"), "\n"), "GH_ENTERPRISE_TOKEN") {
		t.Fatal("GitLab-only store exported GH_ENTERPRISE_TOKEN")
	}
}

func TestEnvNoSecretsDir(t *testing.T) {
	t.Setenv("SWARMERY_SECRETS_DIR", "")
	t.Setenv("HOME", "")
	if p := Path("github.com"); p == "" {
		if env := Env("github.com"); env != nil {
			t.Fatalf("Env without secrets dir = %v, want nil", env)
		}
	}
}

func TestImportFromCLIGitHub(t *testing.T) {
	isolate(t)
	r := &fakeRunner{stdout: "gho_importedimportedimportedimp01\n"}
	if err := ImportFromCLI(context.Background(), r, KindGitHub, "github.com"); err != nil {
		t.Fatal(err)
	}
	if r.calls[0] != "gh auth token --hostname github.com" {
		t.Fatalf("call = %q", r.calls[0])
	}
	if r.envs[0] != nil {
		t.Fatal("import must run against the operator's own CLI config (nil env)")
	}
	vals, err := Load("github.com")
	if err != nil || vals[GitHubTokenKey] != "gho_importedimportedimportedimp01" {
		t.Fatalf("Load = %v, %v", vals, err)
	}
}

func TestImportFromCLIGitLab(t *testing.T) {
	isolate(t)
	r := &fakeRunner{stderr: "gitlab.com\n  ✓ Logged in to gitlab.com as me\n  ✓ Token: glpat-importedimportedimported0\n"}
	if err := ImportFromCLI(context.Background(), r, KindGitLab, "gitlab.com"); err != nil {
		t.Fatal(err)
	}
	if r.calls[0] != "glab auth status --hostname gitlab.com --show-token" {
		t.Fatalf("call = %q", r.calls[0])
	}
	vals, err := Load("gitlab.com")
	if err != nil || vals[GitLabTokenKey] != "glpat-importedimportedimported0" {
		t.Fatalf("Load = %v, %v", vals, err)
	}
}

func TestImportFromCLIFailures(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	failing := &fakeRunner{stderr: "token ghp_leakedleakedleakedleakedle01 rejected", err: fmt.Errorf("exit status 1")}
	err := ImportFromCLI(ctx, failing, KindGitHub, "github.com")
	if err == nil || strings.Contains(err.Error(), "ghp_leaked") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("gh failure err = %v (must be redacted)", err)
	}
	if err := ImportFromCLI(ctx, &fakeRunner{err: fmt.Errorf("exit status 1")}, KindGitLab, "gitlab.com"); err == nil {
		t.Fatal("glab failure not reported")
	}
	if err := ImportFromCLI(ctx, &fakeRunner{stdout: "no token here"}, KindGitLab, "gitlab.com"); err == nil {
		t.Fatal("glab without a token line not reported")
	}
	if err := ImportFromCLI(ctx, &fakeRunner{stdout: "\n"}, KindGitHub, "github.com"); err == nil {
		t.Fatal("empty gh token not reported")
	}
	if err := ImportFromCLI(ctx, &fakeRunner{}, "bitbucket", "bitbucket.org"); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestRedact(t *testing.T) {
	isolate(t)
	cases := []string{
		"ghp_" + strings.Repeat("a", 36),
		"gho_" + strings.Repeat("B", 36),
		"ghu_" + strings.Repeat("1", 30),
		"ghs_" + strings.Repeat("c", 30),
		"ghr_" + strings.Repeat("d", 30),
		"github_pat_" + strings.Repeat("A1_", 20),
		"glpat-" + strings.Repeat("x-_", 8),
	}
	for _, tok := range cases {
		got := Redact("remote: token " + tok + " denied")
		if strings.Contains(got, tok) || got != "remote: token *** denied" {
			t.Errorf("Redact(%q) = %q", tok, got)
		}
	}
	// A short ghp_ lookalike is not a token shape and stays.
	if got := Redact("ghp_short"); got != "ghp_short" {
		t.Errorf("Redact(ghp_short) = %q", got)
	}
	if Redact("") != "" {
		t.Error("Redact(\"\") changed")
	}
}

func TestRedactLoadedLiteral(t *testing.T) {
	dir := isolate(t)
	literal := "plain-literal-secret-9f8e7d"
	if got := Redact("x " + literal); !strings.Contains(got, literal) {
		t.Fatalf("literal masked before it was ever loaded: %q", got)
	}
	// Written by hand, not via Write, so only Load/Env can teach Redact about it.
	if err := os.WriteFile(filepath.Join(dir, "vcs-git.corp.env"), []byte("GH_TOKEN="+literal+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = Env("git.corp")
	if got := Redact("fatal: auth " + literal + " bad"); got != "fatal: auth *** bad" {
		t.Fatalf("Redact(loaded literal) = %q", got)
	}
	// Overlapping literals: the longer one is masked whole.
	remember("plain-literal")
	if got := Redact(literal); got != "***" {
		t.Fatalf("Redact(overlap) = %q", got)
	}
	// Too-short literals are never registered.
	remember("short")
	if got := Redact("short"); got != "short" {
		t.Fatalf("short literal was masked: %q", got)
	}
}

func TestRedactedTail(t *testing.T) {
	if got := RedactedTail("", fmt.Errorf("boom"), tailBytes); got != "boom" {
		t.Fatalf("tail = %q", got)
	}
	long := strings.Repeat("x", 3000) + "END"
	if got := RedactedTail(long, nil, tailBytes); len(got) != 2048 || !strings.HasSuffix(got, "END") {
		t.Fatalf("tail len = %d", len(got))
	}
	if got := RedactedTail("abc", nil, 0); got != "abc" {
		t.Fatalf("max 0 = %q", got)
	}
}

// AssertNoFragment fails when any ≥8-byte substring of tok survives in s.
func assertNoFragment(t *testing.T, s, tok string) {
	t.Helper()
	for i := 0; i+8 <= len(tok); i++ {
		if strings.Contains(s, tok[i:i+8]) {
			t.Fatalf("token fragment %q survived in %q…", tok[i:i+8], s[:min(len(s), 80)])
		}
	}
}

// A token that straddles the 2048-byte cut must be redacted BEFORE the cut:
// cutting first leaves "…" + 30 token bytes no pattern matches.
func TestRedactedTailTokenAtCutBoundary(t *testing.T) {
	tok := "ghp_" + strings.Repeat("K", 36)
	in := tok + strings.Repeat("f", 2018) // cut lands 10 bytes into the token
	assertNoFragment(t, RedactedTail(in, nil, tailBytes), tok)

	isolate(t)
	r := &fakeRunner{stderr: in, err: fmt.Errorf("exit status 1")}
	err := ImportFromCLI(context.Background(), r, KindGitHub, "github.com")
	if err == nil {
		t.Fatal("failure not reported")
	}
	assertNoFragment(t, err.Error(), tok)
}

func TestImportFromCLIGitLabFailureMasksTokenLine(t *testing.T) {
	isolate(t)
	odd := "customshapetoken0123456789"
	r := &fakeRunner{stderr: "x Token: " + odd + "\nx 401 Unauthorized", err: fmt.Errorf("exit status 1")}
	err := ImportFromCLI(context.Background(), r, KindGitLab, "git.corp")
	if err == nil || strings.Contains(err.Error(), odd) {
		t.Fatalf("glab token leaked: %v", err)
	}
}

// A GitLab import (self-hosted host, token on stdout in older glab versions)
// lands as GITLAB_TOKEN in a 0600 store, and Env then hands glab that token
// plus the isolated GLAB_CONFIG_DIR — and no GitHub variable.
func TestImportFromCLIGitLabStores0600AndEnv(t *testing.T) {
	dir := isolate(t)
	const gl = "gitlab.corp.example"
	if env := Env(gl); env != nil {
		t.Fatalf("Env before import = %v, want nil", env)
	}
	tok := "glpat-selfhostedselfhosted00"
	r := &fakeRunner{stdout: gl + "\n  ✓ Logged in to " + gl + " as me (/home/me/.config/glab-cli/config.yml)\n  ✓ Token: " + tok + "\n"}
	if err := ImportFromCLI(context.Background(), r, KindGitLab, gl); err != nil {
		t.Fatal(err)
	}
	if r.envs[0] != nil {
		t.Fatal("import must run against the operator's own glab config (nil env)")
	}
	info, err := os.Stat(filepath.Join(dir, "vcs-"+gl+".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %04o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "vcs-"+gl+".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != GitLabTokenKey+"="+tok+"\n" {
		t.Fatalf("store = %q", raw)
	}
	env := strings.Join(Env(gl), "\n")
	for _, want := range []string{
		GitLabTokenKey + "=" + tok,
		"GLAB_CONFIG_DIR=" + filepath.Join(dir, "vcs-glab-config"),
	} {
		if !strings.Contains(env, want) {
			t.Errorf("Env missing %q:\n%s", want, env)
		}
	}
	for _, unwanted := range []string{GitHubTokenKey + "=", GitHubEnterpriseTokenKey + "="} {
		if strings.Contains(env, unwanted) {
			t.Errorf("GitLab-only Env carries %q:\n%s", unwanted, env)
		}
	}
	// The imported token is now masked by value too.
	if got := Redact("echo " + tok); strings.Contains(got, tok) {
		t.Fatalf("Redact = %q", got)
	}
}

func TestDeleteRemovesStoreAndToleratesMissing(t *testing.T) {
	dir := isolate(t)
	const host = "github.com"
	tok := "gho_" + strings.Repeat("d", 36)
	if err := Write(host, GitHubTokenKey, tok); err != nil {
		t.Fatal(err)
	}
	if !Has(host) {
		t.Fatal("Has = false after Write")
	}
	if err := Delete(host); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vcs-"+host+".env")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("store still present after Delete: %v", err)
	}
	if Has(host) || Env(host) != nil {
		t.Fatal("Has/Env still see a token after Delete")
	}
	// Absent store: no error, idempotent.
	if err := Delete(host); err != nil {
		t.Fatalf("Delete(absent) = %v", err)
	}
	// A deleted token stays masked by value.
	if got := Redact("x " + tok); strings.Contains(got, tok) {
		t.Fatalf("Redact after Delete = %q", got)
	}
}

func TestDeleteRefusesSymlinkAndNonRegular(t *testing.T) {
	dir := isolate(t)
	real := filepath.Join(t.TempDir(), "real.env")
	if err := os.WriteFile(real, []byte("GH_TOKEN=linked-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "vcs-github.com.env")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Delete("github.com"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Delete(symlink) = %v, want ErrInsecure", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink removed despite refusal: %v", err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("symlink target touched: %v", err)
	}

	if err := os.Mkdir(filepath.Join(dir, "vcs-dir.example.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Delete("dir.example"); !errors.Is(err, ErrInsecure) {
		t.Fatalf("Delete(directory) = %v, want ErrInsecure", err)
	}
}

func TestDeleteNoSecretsDir(t *testing.T) {
	t.Setenv("SWARMERY_SECRETS_DIR", "")
	t.Setenv("HOME", "")
	if err := Delete("github.com"); !errors.Is(err, ErrNoStoreDir) {
		t.Fatalf("Delete without secrets dir = %v, want ErrNoStoreDir", err)
	}
}
