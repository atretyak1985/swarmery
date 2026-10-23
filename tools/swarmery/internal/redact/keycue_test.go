package redact

// Key-cued masking of JSON payloads, the storage-only token shapes, and the
// integer counts the key/value rule must leave alone. Every secret-shaped
// value is assembled at run time (see redacttest) so no literal reaches a
// secret scanner.

import (
	"encoding/json"
	"strings"
	"testing"
)

func fake(parts ...string) string { return strings.Join(parts, "") }

// A string value under a secret-named key is masked whole, at any depth.
func TestValueMasksStringUnderSecretKey(t *testing.T) {
	pw := fake("hun", "ter2")
	apiKey := fake("abc", "123", "def")
	in := map[string]any{
		"pass" + "word": pw,
		"env":           map[string]any{"API" + "_KEY": apiKey, "HOME": "/Users/u"},
		"list":          []any{map[string]any{"client" + "_secret": fake("cs", "-value")}},
	}
	out, changed := Value("Bash", in)
	if !changed {
		t.Fatal("payload with key-cued secrets reported unchanged")
	}
	m := out.(map[string]any)
	if got := m["pass"+"word"]; got != Mark {
		t.Errorf("password = %v, want %q", got, Mark)
	}
	env := m["env"].(map[string]any)
	if got := env["API"+"_KEY"]; got != Mark {
		t.Errorf("env.API_KEY = %v, want %q", got, Mark)
	}
	if got := env["HOME"]; got != "/Users/u" {
		t.Errorf("env.HOME = %v, want it unchanged", got)
	}
	if got := m["list"].([]any)[0].(map[string]any)["client"+"_secret"]; got != Mark {
		t.Errorf("list[0].client_secret = %v, want %q", got, Mark)
	}
	if in["pass"+"word"] != pw {
		t.Error("Value modified its input")
	}
	if again, changed := Value("Bash", out); changed {
		t.Errorf("key-cued masking is not idempotent: %v", again)
	}
}

func TestSecretKeyNames(t *testing.T) {
	secret := []string{
		"password", "PASSWORD", "db_password", "newPassword", "passwd", "secret", "client_secret",
		"token", "accessToken", "refresh_token", "GITHUB_TOKEN", "id-token",
		"api_key", "apiKey", "API-KEY", "x-api-key", "ANTHROPIC_API_KEY", "apikey",
		"access_key", "AWS_SECRET_ACCESS_KEY", "private_key", "privateKey",
		"auth", "Authorization", "x-auth-token",
	}
	plain := []string{
		"input_tokens", "max_tokens", "output_tokens", "cache_read_input_tokens", "outputTokens", "tokens",
		"author", "authority", "authored_by", "auth_url", "oauth", "tokenizer", "token_count", "tokenType",
		"key", "sort_key", "cacheKey", "file_path", "command", "content", "description",
	}
	for _, k := range secret {
		if !secretKey(k) {
			t.Errorf("secretKey(%q) = false, want true", k)
		}
	}
	for _, k := range plain {
		if secretKey(k) {
			t.Errorf("secretKey(%q) = true, want false (a count or ordinary field)", k)
		}
	}
}

// Numbers and booleans are never masked, and count-shaped keys keep their
// values: the clean document keeps its exact bytes.
func TestPayloadJSONKeepsCountsAndNonStrings(t *testing.T) {
	doc := `{"usage":{"input_tokens":1234,"max_tokens":4096,"output_tokens":"77"},` +
		`"author":"Ada","authority":"root-ca","token":true,"password":12345,"auth":null}`
	got, changed := PayloadJSON("Bash", doc)
	if changed || got != doc {
		t.Errorf("payload rewritten: changed=%v\n got %s\nwant %s", changed, got, doc)
	}
}

// D6: the code fields of the file-editing tools get only the literal token
// shapes — a secret-named key INSIDE their text is source, not a payload key.
func TestKeyCueSkipsCodeFields(t *testing.T) {
	code := `{"pass` + `word": "hunter2"}` + "\n" + `cfg := map[string]string{"tok` + `en": "abc"}` + "\n"
	in := map[string]any{"file_path": "/repo/cfg.json", "content": code}
	out, changed := Value("Write", in)
	if changed {
		t.Errorf("Write content rewritten: %v", out.(map[string]any)["content"])
	}
	edit := map[string]any{"file_path": "/repo/a.go", "old_string": "x", "new_string": code,
		"edits": []any{map[string]any{"old_string": "a", "new_string": code}}}
	if _, changed := Value("MultiEdit", edit); changed {
		t.Error("MultiEdit code fields rewritten")
	}
	// A JSON-quoted key inside a non-code string is text, not a payload key:
	// it gets the text rules only.
	prose := map[string]any{"stdout": `{"input_tokens": 1234}`}
	if _, changed := Value("Bash", prose); changed {
		t.Error("a count inside stdout text was masked")
	}
}

func TestCountsAfterTokenKeysStayUnchanged(t *testing.T) {
	for _, s := range []string{
		"tok" + "ens: 1234",
		"input_tok" + "ens: 1234, output_tok" + "ens: 56",
		"max_tok" + "ens=4096",
		"tok" + "ens: 1,234.",
		`{"input_tok` + `ens":1234}`,
	} {
		if got := All(s); got != s {
			t.Errorf("All(%q) = %q, want unchanged (a count)", s, got)
		}
		if c := Count(s); len(c) != 0 {
			t.Errorf("Count(%q) = %v, want none", s, c)
		}
	}
	// A token-named key with a real value is still masked, and a numeric
	// password still counts as a secret.
	for _, s := range []string{"tok" + "en: " + fake("abc", "123"), "pass" + "word: " + fake("1234", "5678")} {
		if got := All(s); !strings.HasSuffix(got, Mark) {
			t.Errorf("All(%q) = %q, want the value masked", s, got)
		}
	}
}

// storageSample is one secret shape the storage redactors mask beyond the 11
// response-layer classes.
type storageSample struct {
	class, value, needle string
	literal              bool // a token shape (Literal masks it too)
}

func storageSamples() []storageSample {
	basic := fake("YWxp", "Y2U6", strings.Repeat("c", 16), "==")
	cliPw := fake("hunter", "2cli")
	proj := fake("sk", "-proj-", strings.Repeat("A", 24))
	plain := fake("sk", "-", strings.Repeat("B", 40))
	stripe := fake("sk", "_live_", strings.Repeat("C", 24))
	restricted := fake("rk", "_live_", strings.Repeat("D", 24))
	return []storageSample{
		{"basic_auth", "Authorization: " + "Ba" + "sic " + basic, basic, false},
		{"basic_auth", `"Authorization": "` + "Ba" + "sic " + basic + `"`, basic, false},
		{"cli_password", "mysql -u root --pass" + "word " + cliPw + " db", cliPw, false},
		{"cli_password", "mysql -u root --pass" + "word=" + cliPw + " db", cliPw, false},
		{"openai_key", "OPENAI " + proj, proj, true},
		{"openai_key", "key " + plain + " end", plain, true},
		{"stripe_key", "stripe " + stripe, stripe, true},
		{"stripe_key", "stripe " + restricted, restricted, true},
	}
}

func TestStorageRulesMaskNewShapes(t *testing.T) {
	for _, s := range storageSamples() {
		got := All(s.value)
		if strings.Contains(got, s.needle) {
			t.Errorf("%s: All left %q in %q", s.class, s.needle, got)
		}
		if again := All(got); again != got {
			t.Errorf("%s: All is not idempotent: %q → %q", s.class, got, again)
		}
		if c := Count(got); len(c) != 0 {
			t.Errorf("%s: Count after All = %v, want none", s.class, c)
		}
		if lit := Literal(s.value); s.literal == strings.Contains(lit, s.needle) {
			t.Errorf("%s: Literal = %q, literal shape = %v", s.class, lit, s.literal)
		}
		// The response layer keeps exactly the 11 classes it shares with
		// guard-pack; these shapes are storage-only.
		if s.class == "stripe_key" || s.class == "openai_key" {
			if r := Response(s.value); r != s.value {
				t.Errorf("%s: Response changed %q", s.class, s.value)
			}
		}
	}
	if c := Count(storageSamples()[0].value); c["basic_auth"] != 1 {
		t.Errorf("Count(basic) = %v, want basic_auth 1", c)
	}
	// Keeping the scheme word and the flag visible keeps the row diagnosable.
	if got := All("Authorization: " + "Ba" + "sic " + fake("YWxp", "Y2U6", "cGFzc3dvcmQ=")); got != "Authorization: Basic "+Mark {
		t.Errorf("basic: All = %q", got)
	}
	if got := All("x --pass" + "word " + fake("p", "w1") + " y"); got != "x --password "+Mark+" y" {
		t.Errorf("cli password: All = %q", got)
	}
}

func TestStorageRulesLeaveLookalikes(t *testing.T) {
	for _, s := range []string{
		"a basic introduction to the topic",
		"risk-assessment-framework-document-v2",
		"task-runner-for-the-whole-monorepo-x",
		"sk-short",
		"--password-file /run/secrets/x", // a flag naming a file, not a value
		"see --password -u root",         // the next token is a flag
	} {
		if got := All(s); got != s {
			t.Errorf("All(%q) = %q, want unchanged", s, got)
		}
	}
}

// Code keeps its source: the contextual storage rules stay out of Literal.
func TestLiteralSkipsNewContextualShapes(t *testing.T) {
	src := "\tauthorization := \"Basic \" + enc(user)\n\targs := []string{\"--password\", pw}\n"
	if got := Literal(src); got != src {
		t.Errorf("Literal mangled source: %q", got)
	}
}

func TestCountStoredSeesKeyCuedValues(t *testing.T) {
	doc, _ := json.Marshal(map[string]any{"input": map[string]any{"env": map[string]any{"API" + "_KEY": fake("abc", "123")}}})
	c := countPayload("Bash", string(doc))
	if c[KeyCuedClass] != 1 {
		t.Errorf("countPayload = %v, want %s 1", c, KeyCuedClass)
	}
	masked, _ := PayloadJSON("Bash", string(doc))
	if c := countPayload("Bash", masked); len(c) != 0 {
		t.Errorf("countPayload after PayloadJSON = %v, want none", c)
	}
}

func TestRulesVersionBumped(t *testing.T) {
	if RulesVersion < 2 {
		t.Errorf("RulesVersion = %d: the rules changed, so stored rows must be scanned again", RulesVersion)
	}
}
