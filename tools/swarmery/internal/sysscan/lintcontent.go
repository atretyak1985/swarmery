package sysscan

// Step-09 pre-write validation: the content lint rules of lint.go extracted
// into a PURE in-memory form. The step-04 linter is coupled to registry rows
// (it reads *_versions and writes config_lint_findings); the write path needs
// the same judgment over CANDIDATE content BEFORE anything touches disk or
// DB — so the rule bodies live here, no queries, no writes.

import (
	"fmt"
	"unicode/utf8"
)

// ContentFinding is one in-memory lint finding over candidate content —
// the `lint: [...]` entries of the step-09 write responses. Warnings never
// block a write; only a frontmatter parse failure does (LintContent's err).
type ContentFinding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"` // info | warn — never error (that is parse_error's tier)
	Message  string `json:"message"`
}

// LintContent validates one candidate agent/skill file content entirely
// in-memory. It returns the frontmatter `name` field ("" when absent — the
// caller's uniqueness check skips then) and the content-rule findings.
//
// A frontmatter that does not parse returns a non-nil error — the API maps
// it to 422 and the write is blocked (unlike the tolerant scanner, which
// keeps such files with a parse_error finding: those arrived from disk,
// while here we refuse to CREATE broken state).
//
// Thresholds honor the same SWARMERY_LINT_* env overrides as the scanner
// (per-Config explicit values are a scanner-only nicety).
func LintContent(kind string, content []byte) (name string, findings []ContentFinding, err error) {
	fm, err := parseFrontmatter(content)
	if err != nil {
		return "", nil, err
	}
	_, body, err := splitFrontmatter(content)
	if err != nil {
		return "", nil, err // unreachable after parseFrontmatter, kept for safety
	}
	name = strField(fm, "name")

	switch kind {
	case KindAgent:
		if !boundariesHeading.Match(body) {
			findings = append(findings, ContentFinding{
				Rule:     RuleAgentNoBoundaries,
				Severity: "warn",
				Message:  "agent body has no Boundaries section",
			})
		}
		if strField(fm, "description") == "" {
			findings = append(findings, ContentFinding{
				Rule:     RuleAgentNoDescription,
				Severity: "warn",
				Message:  "empty description in frontmatter",
			})
		}
	case KindSkill:
		min := envInt(EnvMinSkillDescription, DefaultMinSkillDescription)
		if n := utf8.RuneCountInString(strField(fm, "description")); n < min {
			findings = append(findings, ContentFinding{
				Rule:     RuleSkillShortDesc,
				Severity: "warn",
				Message: fmt.Sprintf("description is %d chars — below the %d-char minimum, it will trigger poorly",
					n, min),
			})
		}
	}
	return name, findings, nil
}
