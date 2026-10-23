package playbooks

import (
	"os"
	"regexp"
	"strings"
)

// A plan phase names the playbook it runs under with a `**Playbook:** <name>`
// header line, e.g. `**Playbook:** long-tests`. The name selects
// guards.Config.PlaybookOverrides (internal/stall reads it to stretch the
// stall threshold of a phase whose tests legitimately run for an hour); it is
// not resolved against the registry, because an override may name a recipe no
// board card uses.

// phaseDocHeaderLines bounds the header scan, as wsingest bounds `**Verify:**`
// and `**Review:**` (docStatusHeaderLines): a phase doc quotes agent prompts
// further down, and a `**Playbook:**` line inside one describes someone else's
// phase.
const phaseDocHeaderLines = 15

// docPlaybookRe matches the header field anywhere on a line, so it also reads
// the `**Repo:** … · **Playbook:** long-tests · …` joined header form.
var docPlaybookRe = regexp.MustCompile("(?i)\\*\\*Playbook:\\*\\*\\s*`?([A-Za-z0-9._-]+)`?")

// ParseDocPlaybook returns the playbook a phase doc's header names, lower-cased,
// or "" when it names none. The scan stops at the first `## ` section.
func ParseDocPlaybook(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > phaseDocHeaderLines {
		lines = lines[:phaseDocHeaderLines]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			break
		}
		if m := docPlaybookRe.FindStringSubmatch(line); m != nil {
			return strings.ToLower(m[1])
		}
	}
	return ""
}

// PhasePlaybook is ParseDocPlaybook over the phase doc at docPath. An unreadable
// doc names no playbook: the caller then applies its default, which is the
// conservative answer for a threshold.
func PhasePlaybook(docPath string) string {
	if docPath == "" {
		return ""
	}
	raw, err := os.ReadFile(docPath)
	if err != nil {
		return ""
	}
	return ParseDocPlaybook(string(raw))
}
