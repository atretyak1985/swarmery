package sysedit

// Step-11: the agent-creation template. Minimal and neutral — a scaffolding
// tool, not a prompting policy. The frontmatter carries ONLY the fields the
// step-01 corpus confirmed (docs/system-config-format.md §1.2): name,
// description, model?, tools?. The body has Role / Boundaries sections; when
// the form gives no boundaries, the template leaves a TODO stub WITHOUT a
// Boundaries heading — so the linter's agent_no_boundaries warning keeps
// highlighting it until a real section is written.

import (
	"bytes"
	"regexp"
	"strconv"
	"text/template"
)

// AgentTemplate carries the create-form fields into RenderAgentMD.
type AgentTemplate struct {
	Name        string
	Description string
	Model       string   // optional
	Tools       []string // optional
	Boundaries  string   // optional — empty → the linted TODO stub
}

// plainScalar matches strings that are safe as unquoted YAML plain scalars;
// anything else is double-quoted (strconv.Quote's escapes for printable text
// are YAML-compatible).
var plainScalar = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ,.;/()'&+_-]*$`)

func yamlScalar(s string) string {
	if plainScalar.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}

var agentTmpl = template.Must(template.New("agent").Funcs(template.FuncMap{
	"yaml": yamlScalar,
}).Parse(`---
name: {{.Name}}
description: {{yaml .Description}}
{{- if .Model}}
model: {{yaml .Model}}
{{- end}}
{{- if .Tools}}
tools:
{{- range .Tools}}
  - {{yaml .}}
{{- end}}
{{- end}}
---

# {{.Name}}

## Role

{{.Description}}
{{if .Boundaries}}
## Boundaries

{{.Boundaries}}
{{else}}
TODO: add a Boundaries section describing what this agent must not do.
{{end}}`))

// RenderAgentMD renders the canonical agent markdown for a create request.
// The caller re-validates the result through sysscan.LintContent — the same
// gate every PUT goes through — before anything touches disk.
func RenderAgentMD(t AgentTemplate) ([]byte, error) {
	var buf bytes.Buffer
	if err := agentTmpl.Execute(&buf, t); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
