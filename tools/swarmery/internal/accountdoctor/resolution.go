package accountdoctor

// Arm (a) — the resolution, said out loud: which account, which rung decided
// it, which estate, and — for the default account — WHICH of its two
// .claude.json profiles a session will read. The account and the estate are
// already on the Report (doctor.go); this arm adds the admission line, the
// default profile, and the descendant pins that shadow the estate's account.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

// ProfileFile is one candidate .claude.json: where it is and what it holds, by
// size and project count only.
type ProfileFile struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Bytes    int64  `json:"bytes"`
	Projects int    `json:"projects"` // entries in its top-level `projects` object
	Mtime    string `json:"mtime"`    // RFC3339 UTC, "" when absent
}

// DefaultProfile is G6 made visible: the default account is TWO profiles. With
// CLAUDE_CONFIG_DIR unset the CLI reads ~/.claude.json; with
// CLAUDE_CONFIG_DIR=<configDir> it reads <configDir>/.claude.json — different
// files, and on macOS a different keychain item. Reads names the one a session
// resolved here will use; nothing here sets the variable to "fix" it.
type DefaultProfile struct {
	Home      ProfileFile `json:"home"`      // ~/.claude.json
	ConfigDir ProfileFile `json:"configDir"` // <configDir>/.claude.json
	Reads     string      `json:"reads"`     // the Path of exactly one of the two
	Why       string      `json:"why"`
	// CredentialSources is where swarmery's own credential lookup for the
	// default account looks (usage.CredentialSourcesFor) — paths and keychain
	// item NAMES, never a credential.
	CredentialSources []string `json:"credentialSources"`
}

// maxProfileBytes bounds how much of a .claude.json the project count reads.
const maxProfileBytes = 64 << 20

// resolution fills Admission, DefaultProfile and the pin findings.
func (r *run) resolution() {
	res, rep := r.res, r.rep
	lines := res.AdmissionLines()
	if len(lines) == 0 {
		lines = res.IgnoredLines()
	}
	rep.Admission = strings.Join(lines, "\n")

	if res.DefaultProfile {
		rep.DefaultProfile = defaultProfile(res)
	}

	if res.EstateRoot == "" {
		return
	}
	estateAccount := claudeacct.Binding(res.EstateRoot)
	if estateAccount == "" {
		estateAccount = ingest.DefaultAccount
	}
	for _, e := range claudeacct.ScanPinsDetail(res.EstateRoot) {
		file := filepath.Join(e.Dir, filepath.FromSlash(claudeacct.BindingFile))
		switch {
		case e.Ignored != "":
			// Listed as IGNORED with its reason — never as a shadowing pin.
			r.add(Finding{ID: "binding-ignored", Severity: SevInfo,
				Title: "a binding under the estate is ignored", Detail: e.Ignored, File: file})
		case e.Key != estateAccount:
			r.add(Finding{ID: "shadowed-pin", Severity: SevInfo,
				Title: fmt.Sprintf("a descendant pin overrides the estate's account (%s)", estateAccount),
				Detail: fmt.Sprintf("%s pins %s; every path below it runs under %s, not %s",
					e.Dir, e.Key, e.Key, estateAccount),
				File: file})
		}
	}
}

// defaultProfile describes both candidate profiles and picks the one read.
func defaultProfile(res claudeacct.Resolution) *DefaultProfile {
	home, _ := userHomeDir()
	cfg := accountConfigDir(ingest.DefaultAccount)
	p := &DefaultProfile{
		Home:              profileFile(filepath.Join(home, ".claude.json")),
		ConfigDir:         profileFile(filepath.Join(cfg, ".claude.json")),
		CredentialSources: usage.CredentialSourcesFor(usage.Source{Account: ingest.DefaultAccount, IgnoreConfigDirEnv: true}),
	}
	if p.CredentialSources == nil {
		p.CredentialSources = []string{}
	}
	// An explicit default binding makes every spawn drop an inherited
	// CLAUDE_CONFIG_DIR (claudeacct.SpawnEnvResolved), so the CLI reads the
	// HOME profile. An unbound path keeps whatever this environment carries.
	inherited := strings.TrimSpace(getenv("CLAUDE_CONFIG_DIR"))
	switch {
	case res.Account == "" && inherited != "":
		p.Reads = filepath.Join(filepath.Clean(inherited), ".claude.json")
		p.Why = "nothing binds this path, and this environment carries CLAUDE_CONFIG_DIR, which the CLI keeps"
	case res.Account == "":
		p.Reads = p.Home.Path
		p.Why = "nothing binds this path and CLAUDE_CONFIG_DIR is unset, so the CLI reads the home profile"
	default:
		p.Reads = p.Home.Path
		p.Why = "the binding names the default account, so a swarmery launch removes CLAUDE_CONFIG_DIR and the CLI reads the home profile"
	}
	return p
}

// profileFile stats one .claude.json and counts its projects — the file can be
// large, so the count streams the top-level object and never holds it whole.
func profileFile(path string) ProfileFile {
	pf := ProfileFile{Path: path}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return pf
	}
	pf.Exists = true
	pf.Bytes = fi.Size()
	pf.Mtime = fi.ModTime().UTC().Format(time.RFC3339)
	f, err := os.Open(path)
	if err != nil {
		return pf
	}
	defer f.Close()
	pf.Projects = countProjects(io.LimitReader(f, maxProfileBytes))
	return pf
}

// countProjects is the number of keys in the top-level `projects` object of a
// .claude.json stream; 0 on any doubt. No value is kept.
func countProjects(rd io.Reader) int {
	dec := json.NewDecoder(rd)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return 0
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return 0
		}
		if name, _ := t.(string); name != "projects" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return 0
			}
			continue
		}
		var projects map[string]json.RawMessage
		if dec.Decode(&projects) != nil {
			return 0
		}
		return len(projects)
	}
	return 0
}
