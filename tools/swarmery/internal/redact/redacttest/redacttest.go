// Package redacttest builds secret-shaped sample values for the tests of the
// redactors and of their callers (ingest, the rescrub worker, the count tool).
//
// Every value is assembled at run time by string concatenation, so neither a
// test source nor a fixture holds a literal that a secret scanner or a write
// guard would flag or rewrite. None of the values is a real credential.
package redacttest

import "strings"

// Sample is one secret-shaped value of one rule class.
type Sample struct {
	// Class is the rule name (internal/redact RuleNames, or "private_key").
	Class string
	// Value is the text as it would appear in a transcript.
	Value string
	// Needle is the secret part of Value: the substring that must not survive
	// redaction (for the contextual classes the key word stays visible).
	Needle string
}

// Samples returns one sample per rule class, in rule order, then a PEM
// private-key block.
func Samples() []Sample {
	anthropic := "sk-" + "ant-" + "api03-" + strings.Repeat("Q", 24)
	github := "gh" + "p_" + strings.Repeat("R", 36)
	gitlab := "gl" + "pat-" + strings.Repeat("S", 20)
	aws := "AK" + "IA" + strings.Repeat("T", 16)
	slack := "xo" + "xb-" + "1234-" + strings.Repeat("U", 12)
	google := "AI" + "za" + strings.Repeat("V", 35)
	npm := "np" + "m_" + strings.Repeat("W", 36)
	jwt := "ey" + "J" + strings.Repeat("X", 12) + "." + strings.Repeat("Y", 10) + "." + strings.Repeat("Z", 10)
	bearerValue := strings.Repeat("b", 24)
	kvValue := "hunter" + "2hunter2"
	urlPass := "s3cr3t" + "pw"
	pemBody := "MIIE" + "ow" + strings.Repeat("P", 40)
	return []Sample{
		{"anthropic_key", anthropic, anthropic},
		{"github_token", github, github},
		{"gitlab_token", gitlab, gitlab},
		{"aws_access_key", aws, aws},
		{"slack_token", slack, slack},
		{"google_api_key", google, google},
		{"npm_token", npm, npm},
		{"jwt", jwt, jwt},
		{"bearer", "Authorization: " + "Bea" + "rer " + bearerValue, bearerValue},
		{"generic_key_value", "pass" + "word=" + kvValue, kvValue},
		{"url_userinfo", "https://" + "alice:" + urlPass + "@db.example.com/x", urlPass},
		{"private_key", "-----BEGIN " + "RSA PRIV" + "ATE KEY-----\n" + pemBody + "\n" + pemBody +
			"\n-----END " + "RSA PRIV" + "ATE KEY-----", pemBody},
	}
}

// Joined is every sample's Value on its own line, framed by ordinary words.
func Joined() string {
	var b strings.Builder
	b.WriteString("before the samples\n")
	for _, s := range Samples() {
		b.WriteString(s.Value)
		b.WriteString("\n")
	}
	b.WriteString("after the samples")
	return b.String()
}

// LiteralJoined is Joined without the two contextual classes (bearer,
// generic_key_value): the secrets a redactor of CODE is expected to mask.
// Inside code those two classes are left alone by design — an assignment to a
// variable named token is source, not a credential.
func LiteralJoined() string {
	var b strings.Builder
	b.WriteString("before the samples\n")
	for _, s := range Samples() {
		if s.Class == "bearer" || s.Class == "generic_key_value" {
			continue
		}
		b.WriteString(s.Value)
		b.WriteString("\n")
	}
	b.WriteString("after the samples")
	return b.String()
}

// Needles is every sample's Needle.
func Needles() []string {
	ss := Samples()
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Needle
	}
	return out
}

// SourceAssignment is ordinary source code that the contextual rules match
// and the literal rules must leave alone: an assignment of a function call to
// a variable named token.
func SourceAssignment() string { return "\t" + "tok" + "en := " + "loadToken(cfg)\n" }
