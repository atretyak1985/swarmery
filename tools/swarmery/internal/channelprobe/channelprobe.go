// Package channelprobe reads, stores and compares the result of
// scripts/tests/cc-channel-probe.sh — the measurement of which configuration
// channels the installed `claude` CLI actually honours for a plugin-shipped
// .mcp.json:
//
//	G1 — which settings tiers `pluginConfigs` (${user_config.KEY}) is read from;
//	G2 — that an unset ${VAR} is substituted LITERALLY, and what the CLI says
//	     about it;
//	G3 — which settings tiers an `env` block expands ${VAR} from.
//
// It is NOT internal/claudeprobe, which answers a different question ("can the
// CLI authenticate under this config dir?"). The two share nothing but a verb.
//
// Every fact here is CLI behaviour, so it moves when the CLI is upgraded. The
// harness stamps each result with `claude --version` and this package compares
// it against the baseline embedded in the binary (baseline.json), so a CLI that
// falsifies a fact produces a named Drift carrying the version that did it.
//
// A result is a plain file under Path(), held to the same hygiene as the
// per-account secret store (internal/claudeacct/secrets.go): dir 0700, file 0600,
// and a file readable beyond its owner is refused on Load. It never carries a
// value from the operator's environment — Validate enforces that by shape.
package channelprobe

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SchemaVersion is the result shape this package reads and writes. The harness
// writes the same number; a result with any other schema is refused rather than
// half-read.
const SchemaVersion = 1

// Verdict is the per-fact outcome. The ladder is deliberately three-valued: a
// probe that could not see its evidence says Inconclusive and is never rounded
// up to Pass.
type Verdict string

const (
	VerdictPass         Verdict = "pass"
	VerdictDrift        Verdict = "drift"
	VerdictInconclusive Verdict = "inconclusive"
)

// Fact is one measured fact: the verdict the harness reached, the boolean
// observations it rests on (an observation the harness could not make is
// ABSENT, never false), and a short human note.
type Fact struct {
	Verdict  Verdict         `json:"verdict"`
	Observed map[string]bool `json:"observed,omitempty"`
	Note     string          `json:"note,omitempty"`
}

// Result is one harness run.
type Result struct {
	Schema int `json:"schema"`
	// CLIVersion is the bare semver parsed from `claude --version` ("2.1.280");
	// CLIVersionRaw is that command's output verbatim ("2.1.280 (Claude Code)").
	CLIVersion    string          `json:"cliVersion"`
	CLIVersionRaw string          `json:"cliVersionRaw,omitempty"`
	CLIPath       string          `json:"cliPath,omitempty"`
	MeasuredAt    time.Time       `json:"measuredAt"`
	Host          string          `json:"host,omitempty"`
	Method        string          `json:"method"`
	Facts         map[string]Fact `json:"facts"`
}

const (
	// probesDirEnv overrides the result directory, for the same reason
	// SWARMERY_SECRETS_DIR exists: no test may write the operator's ~/.swarmery.
	probesDirEnv = "SWARMERY_PROBES_DIR"
	dirMode      = 0o700
	fileMode     = 0o600
	// groupOther is every permission bit outside the owner's; a result file or
	// directory carrying any of them is refused.
	groupOther = 0o077
)

//go:embed baseline.json
var baselineJSON []byte

// Baseline is the expected result embedded in the binary: what the facts were
// when last confirmed, and therefore what a fresh run is compared against. It
// travels with the binary so a fresh machine has something to compare with.
func Baseline() (Result, error) {
	return Parse(baselineJSON)
}

// Path is the directory holding probe results: $SWARMERY_PROBES_DIR when set,
// else ~/.swarmery/probes. "" when neither resolves.
func Path() string {
	if dir := strings.TrimSpace(os.Getenv(probesDirEnv)); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".swarmery", "probes")
}

// versionName is what a CLI version may look like to become a file name. The
// harness applies the same rule, so both sides agree on "<cliVersion>.json".
var versionName = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]*$`)

// FileName is the result file name for a CLI version: "<cliVersion>.json", or
// "unknown.json" when the version is empty or not safe as a bare file name.
func FileName(cliVersion string) string {
	v := strings.TrimSpace(cliVersion)
	if !versionName.MatchString(v) || strings.Contains(v, "..") {
		v = "unknown"
	}
	return v + ".json"
}

// Parse decodes a harness result and refuses one this package cannot trust:
// anything Validate's rules reject (checked on the raw bytes), a different
// schema, no facts, or an unknown verdict.
func Parse(data []byte) (Result, error) {
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{}, fmt.Errorf("channelprobe: decode result: %w", err)
	}
	// The raw bytes, not just the decoded struct: see validateJSON.
	if err := validateJSON(data); err != nil {
		return Result{}, err
	}
	if r.Schema != SchemaVersion {
		return Result{}, fmt.Errorf("channelprobe: result schema %d, want %d", r.Schema, SchemaVersion)
	}
	if len(r.Facts) == 0 {
		return Result{}, errors.New("channelprobe: result carries no facts")
	}
	for name, f := range r.Facts {
		switch f.Verdict {
		case VerdictPass, VerdictDrift, VerdictInconclusive:
		default:
			return Result{}, fmt.Errorf("channelprobe: fact %s has unknown verdict %q", name, f.Verdict)
		}
	}
	return r, nil
}

// Save writes r into dir as FileName(r.CLIVersion), creating dir at 0700 and the
// file at 0600, and returns the file's path. The write is atomic (temp file +
// rename) so a reader never sees half a result. r is validated first: a result
// that could leak a value is never written.
func Save(dir string, r Result) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("channelprobe: no probes directory")
	}
	if err := Validate(r); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("channelprobe: encode result: %w", err)
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("channelprobe: create %s: %w", dir, err)
	}
	// MkdirAll leaves an existing directory's mode alone; the store's contract is
	// 0700 regardless of who created it first.
	if err := os.Chmod(dir, dirMode); err != nil {
		return "", fmt.Errorf("channelprobe: chmod %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".result-*.tmp") // CreateTemp opens at 0600
	if err != nil {
		return "", fmt.Errorf("channelprobe: temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return "", fmt.Errorf("channelprobe: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("channelprobe: close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, fileMode); err != nil {
		return "", fmt.Errorf("channelprobe: chmod %s: %w", tmpName, err)
	}
	path := filepath.Join(dir, FileName(r.CLIVersion))
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("channelprobe: rename into %s: %w", path, err)
	}
	return path, nil
}

// Load reads one result file. It refuses a file — or a containing directory —
// readable beyond its owner, the same ceiling the secret store's loader enforces:
// a store that silently tolerates 0644 is not a store with a hygiene contract.
func Load(path string) (Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, fmt.Errorf("channelprobe: %w", err)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("channelprobe: %s is a directory", path)
	}
	if mode := info.Mode().Perm(); mode&groupOther != 0 {
		return Result{}, fmt.Errorf("channelprobe: refusing %s: mode %04o is readable beyond its owner (want %04o)",
			path, mode, fileMode)
	}
	dir := filepath.Dir(path)
	if dinfo, err := os.Stat(dir); err == nil {
		if mode := dinfo.Mode().Perm(); mode&groupOther != 0 {
			return Result{}, fmt.Errorf("channelprobe: refusing %s: directory %s has mode %04o (want %04o)",
				path, dir, mode, dirMode)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("channelprobe: %w", err)
	}
	return Parse(data)
}
