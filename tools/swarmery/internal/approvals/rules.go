package approvals

// Auto-approve rules (control-plane v2). A rule's tool_pattern is either
//
//	Tool           — exact tool_name match, any input (e.g. `Read`)
//	Tool(argGlob)  — exact tool_name AND the tool's argument string matches
//	                 the glob (e.g. `Bash(git *)`); `*` matches ANY run of
//	                 characters, including spaces and '/'
//
// Matching is deny-by-default:
//   - no wildcard in the tool part — `*` or `Ba*sh` do not parse;
//   - Tool(argGlob) only matches tools with a known argument field
//     (toolArgFields in toolarg.go); unknown tools / missing fields never match;
//   - AskUserQuestion is never auto-approvable: an answerless approve is the
//     E12d failure mode — rejected at parse AND skipped by the evaluator.
//
// SECURITY: `Bash(git *)` is a PREFIX match on tool_input.command — it also
// matches `git status && rm -rf /`. Keep rules narrow; every auto-approval
// keeps its permission_requests row (resolved_via='rule') as the audit trail.

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
)

// rulePatternRe: a literal tool identifier (letters/digits/_/-, no
// wildcards), then an optional non-empty (argGlob).
var rulePatternRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*)(?:\((.+)\))?$`)

// RulePattern is one parsed approval_rules.tool_pattern.
type RulePattern struct {
	Tool     string // exact tool name
	Inner    string // argument glob; meaningful only when HasInner
	HasInner bool
}

// ParseRulePattern validates and parses a tool_pattern. Shared by the
// evaluator and by POST /api/approval-rules input validation.
func ParseRulePattern(s string) (RulePattern, error) {
	s = strings.TrimSpace(s)
	m := rulePatternRe.FindStringSubmatch(s)
	if m == nil {
		return RulePattern{}, fmt.Errorf(
			"invalid tool pattern %q: want Tool or Tool(argGlob); the tool part is a literal name (no wildcards)", s)
	}
	if m[1] == AskUserQuestionTool {
		return RulePattern{}, fmt.Errorf(
			"%s cannot be auto-approved — answers are required (E12d)", AskUserQuestionTool)
	}
	return RulePattern{Tool: m[1], Inner: m[2], HasInner: m[2] != ""}, nil
}

// Matches reports whether one tool call satisfies the pattern.
func (r RulePattern) Matches(toolName string, toolInput json.RawMessage) bool {
	if toolName != r.Tool {
		return false
	}
	if !r.HasInner {
		return true
	}
	arg, ok := argOf(toolName, toolInput)
	if !ok {
		return false // deny-by-default: no known argument field → no match
	}
	return globMatch(r.Inner, arg)
}

// globMatch implements the rule glob: '*' matches any run of characters
// (including '/', spaces, newlines); everything else is literal. Deliberately
// NOT path.Match — command strings and URLs contain '/' freely.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return s == pattern // no wildcard → exact match
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		if mid == "" {
			continue // consecutive '*' collapse
		}
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	last := parts[len(parts)-1]
	return last == "" || strings.HasSuffix(s, last)
}

// matchRuleLocked returns the id of the FIRST enabled rule (by id order)
// matching this hook call within the session's project scope (global
// project_id-NULL rules included), or 0. Runs under s.mu — one more short
// DB round-trip on the Open path, same as every other query there.
func (s *Service) matchRuleLocked(sessionID int64, in HookInput) int64 {
	if in.ToolName == AskUserQuestionTool {
		return 0 // never auto-approve questions (E12d)
	}
	var projectID int64
	if err := s.db.QueryRow(
		`SELECT project_id FROM sessions WHERE id = ?`, sessionID).Scan(&projectID); err != nil {
		log.Printf("warn: approvals: project lookup for session %d: %v", sessionID, err)
		return 0 // deny-by-default
	}
	rows, err := s.db.Query(
		`SELECT id, tool_pattern FROM approval_rules
		 WHERE enabled = 1 AND action = 'approve'
		   AND (project_id IS NULL OR project_id = ?)
		 ORDER BY id`, projectID)
	if err != nil {
		log.Printf("warn: approvals: rule query: %v", err)
		return 0
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var pattern string
		if err := rows.Scan(&id, &pattern); err != nil {
			log.Printf("warn: approvals: rule scan: %v", err)
			return 0 // deny-by-default
		}
		p, err := ParseRulePattern(pattern)
		if err != nil {
			log.Printf("warn: approvals: skipping rule %d: %v", id, err)
			continue
		}
		if p.Matches(in.ToolName, in.ToolInput) {
			return id
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("warn: approvals: rule iteration: %v", err)
	}
	return 0
}
