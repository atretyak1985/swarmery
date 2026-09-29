package accountdoctor

// The ONE choke point every printed string goes through. No arm puts a value
// into a Report — but "no arm does" is a property of today's code, and the
// invariant ("no secret VALUE anywhere, in any format") has to survive
// tomorrow's. So both renderers pass their whole output through redact(),
// which replaces every occurrence of every value a credential could have —
// each value in every loadable store under SecretsDir(), and the value this
// environment holds for every variable the enabled packs reference — with a
// fixed marker. render_redaction_test.go plants a value in a report and fails
// the moment redact() is bypassed.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// redactedMarker replaces a value; it carries no JSON-special character, so a
// replacement inside a JSON string keeps the document valid.
const redactedMarker = "[redacted]"

// minRedactLen: a value this short ("1", "on") would shred ordinary text and
// cannot be a credential worth the name. Longer values are always replaced.
const minRedactLen = 4

// RenderJSON writes the report as one JSON object and a newline.
func RenderJSON(w io.Writer, rep Report) error {
	raw, err := json.Marshal(rep)
	if err != nil {
		return err
	}
	raw = redactJSON(raw, secretValues(rep))
	_, err = w.Write(append(raw, '\n'))
	return err
}

// RenderText writes the human form: names, paths and counts.
func RenderText(w io.Writer, rep Report) error {
	var b strings.Builder
	text(&b, rep)
	_, err := io.WriteString(w, redactText(b.String(), secretValues(rep)))
	return err
}

func text(b *strings.Builder, r Report) {
	p := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	p("project:      %s\n", r.Path)
	p("account:      %s (%s)\n", r.Account, r.Source)
	p("config dir:   %s\n", orDash(r.ConfigDir))
	if r.Estate != "" {
		p("estate:       %s (root %s)\n", r.Estate, r.EstateRoot)
	}
	for _, line := range strings.Split(r.Admission, "\n") {
		if line != "" {
			p("%s\n", line)
		}
	}
	if dp := r.DefaultProfile; dp != nil {
		for _, pf := range []ProfileFile{dp.Home, dp.ConfigDir} {
			if pf.Exists {
				p("profile:      %s — %d bytes, %d projects, modified %s\n", pf.Path, pf.Bytes, pf.Projects, pf.Mtime)
			} else {
				p("profile:      %s — absent\n", pf.Path)
			}
		}
		p("reads:        %s (%s)\n", dp.Reads, dp.Why)
	}
	p("enabled:      %d pack(s)\n", len(r.EnabledPacks))
	p("credentials:  %d supplied by the estate's store\n", r.Credentials)
	p("coverage:     %d of %d referenced variable(s) set\n", len(r.VarsPresent), len(r.VarsExpected))
	if len(r.VarsExpected) > 0 {
		p("referenced:   %s\n", strings.Join(r.VarsExpected, ", "))
	}
	if len(r.VarsMissing) > 0 {
		p("missing:      %s\n", strings.Join(r.VarsMissing, ", "))
	}
	p("launched via swarmery: %s\n", yesNo(r.LaunchedViaSwarmery))
	if r.Daemon {
		p("daemon worktree: yes\n")
	}
	for _, d := range r.SettingsDelta {
		if d.Kind == "only-in" {
			p("settings:     %s — %d name(s) only in %s, not %s: %s\n", d.Key, d.Count, d.OnlyIn, d.Other, strings.Join(d.Names, ", "))
		} else {
			p("settings:     %s — %d name(s) differ between %s: %s\n", d.Key, d.Count, strings.Join(d.Between, " and "), strings.Join(d.Names, ", "))
		}
	}
	for _, e := range r.Parity {
		accts := make([]string, 0, len(e.Versions))
		for a := range e.Versions {
			accts = append(accts, a)
		}
		sort.Strings(accts)
		var parts []string
		for _, a := range accts {
			parts = append(parts, a+"="+strings.Join(e.Versions[a], "|"))
		}
		p("parity:       %s [%s] %s — %s\n", e.ID, e.Scope, e.Kind, strings.Join(parts, " "))
	}
	for _, d := range r.StaleDuplicates {
		key := ""
		if d.Key != "" {
			key = " " + d.Key
		}
		p("duplicate:    %s%s — %d name(s): %s\n", d.Kind, key, d.Count, strings.Join(d.Overlap, ", "))
		for _, path := range d.Paths {
			p("                %s\n", path)
		}
	}
	for _, f := range r.Findings {
		p("[%s] %s: %s\n", f.Severity, f.ID, f.Title)
		if f.Detail != "" {
			p("        %s\n", f.Detail)
		}
		if f.File != "" {
			p("        file: %s\n", f.File)
		}
	}
}

// secretValues is every value to refuse: each value in every loadable store,
// and this environment's value for every referenced variable. Longest first,
// so a value containing another is replaced whole.
func secretValues(rep Report) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if len(v) >= minRedactLen && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, s := range listStores() {
		if s.state != claudeacct.StorePresent {
			continue
		}
		for _, kv := range claudeacct.SecretEnvForStore(s.key) {
			_, v, _ := strings.Cut(kv, "=")
			add(v)
		}
	}
	for _, name := range rep.VarsExpected {
		add(getenv(name))
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// redactText replaces every raw occurrence of every value.
func redactText(s string, values []string) string {
	for _, v := range values {
		s = strings.ReplaceAll(s, v, redactedMarker)
	}
	return s
}

// redactJSON replaces every occurrence of every value in its JSON-escaped
// spelling — the form it would take inside a marshalled string.
func redactJSON(raw []byte, values []string) []byte {
	for _, v := range values {
		esc, err := json.Marshal(v)
		if err != nil || len(esc) < 2 {
			continue
		}
		raw = bytes.ReplaceAll(raw, esc[1:len(esc)-1], []byte(redactedMarker))
	}
	return raw
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
