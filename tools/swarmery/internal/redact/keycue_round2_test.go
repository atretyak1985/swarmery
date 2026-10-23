package redact

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// More key words: pass / pwd only as a word after another word (PWD alone is
// the shell's working directory), cookies, and the <noun>_key secrets.
func TestSecretKeyNamesMoreCues(t *testing.T) {
	secret := []string{
		"pass", "db_pass", "smtpPass", "db_pwd", "smtpPwd", "MYSQL_PWD",
		"cookie", "Set-Cookie", "session_cookie",
		"session_key", "signing_key", "encryptionKey", "MASTER_KEY", "client_key", "secret_key",
		"password_hint_shown", // password anywhere: a string under it is masked, a bool is not
	}
	plain := []string{
		"passed", "bypass", "pass_through", "pass_rate", "compass", "passing_tests",
		"PWD", "pwd", "OLDPWD", "cookie_count", "cache_key", "primary_key", "sort_key", "idempotency_key",
	}
	for _, k := range secret {
		if !secretKey(k) {
			t.Errorf("secretKey(%q) = false, want true", k)
		}
	}
	for _, k := range plain {
		if secretKey(k) {
			t.Errorf("secretKey(%q) = true, want false", k)
		}
	}
}

// A string array under a secret key is masked element by element; numbers in
// it stay.
func TestValueMasksStringArraysUnderSecretKey(t *testing.T) {
	in := map[string]any{
		"API" + "_KEY": []any{fake("k1", "abc"), fake("k2", "def"), 3},
		"env":          map[string]any{"pass" + "words": []string{fake("p1", "xyz"), fake("p2", "xyz")}},
		"tok" + "en":   []any{},
	}
	out, changed := Value("Bash", in)
	if !changed {
		t.Fatal("string arrays under secret keys reported unchanged")
	}
	m := out.(map[string]any)
	if got, want := m["API"+"_KEY"], []any{Mark, Mark, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("API_KEY = %v, want %v", got, want)
	}
	if got, want := m["env"].(map[string]any)["pass"+"words"], []string{Mark, Mark}; !reflect.DeepEqual(got, want) {
		t.Errorf("env.passwords = %v, want %v", got, want)
	}
	if _, changed := Value("Bash", out); changed {
		t.Error("masking string arrays is not idempotent")
	}
	if c := countPayload("Bash", `{"API_KEY":["a1","b2",3]}`); c[KeyCuedClass] != 2 {
		t.Errorf("countPayload = %v, want %s 2", c, KeyCuedClass)
	}
}

// Plural cue words (secrets, passwords, tokens, cookies, apiKeys) name a
// secret only when the value is a string that is not a number: a count stays
// visible, as a number or as a numeric string.
func TestValuePluralKeysMaskOnlyNonNumericStrings(t *testing.T) {
	masked := map[string]any{
		"secr" + "ets": fake("s", "3cr3t"), "tok" + "ens": fake("abc", "12"), "cook" + "ies": "a=b",
		"api" + "Keys": []any{fake("k", "-1"), fake("k", "-2")},
	}
	out, changed := Value("Bash", masked)
	if !changed {
		t.Fatal("plural secret keys reported unchanged")
	}
	for k, v := range out.(map[string]any) {
		if arr, ok := v.([]any); ok {
			for _, e := range arr {
				if e != Mark {
					t.Errorf("%s element = %v, want %q", k, e, Mark)
				}
			}
			continue
		}
		if v != Mark {
			t.Errorf("%s = %v, want %q", k, v, Mark)
		}
	}
	kept := `{"tokens":1234,"tokens_b":"77","input_tokens":"1234","secrets":12,"passwords":["12","34"],"cookies":"0"}`
	if got, changed := PayloadJSON("Bash", kept); changed {
		t.Errorf("numeric values under plural keys were masked: %s", got)
	}
}

func TestTextRulesMoreFlagsAndSchemes(t *testing.T) {
	tok := fake("abc", "123", "xyz")
	for _, c := range []struct{ in, want string }{
		{"gh --tok" + "en " + tok + " x", "gh --token " + Mark + " x"},
		{"gh --tok" + "en=" + tok, "gh --token=" + Mark},
		{"cli --api-k" + "ey " + tok, "cli --api-key " + Mark},
		{"cli --api_k" + "ey=" + tok, "cli --api_key=" + Mark},
		{"vault --sec" + "ret " + tok, "vault --secret " + Mark},
		{"Authorization: " + "Tok" + "en " + tok, "Authorization: Token " + Mark},
		{`"authorization": "tok` + `en ` + tok + `"`, `"authorization": "token ` + Mark + `"`},
		// A numeric value after --token is an id or a count; a numeric
		// password is still a password.
		{"tool --tok" + "en 1234", "tool --token 1234"},
		{"mysql --pass" + "word 1234", "mysql --password " + Mark},
		// Flags that name a file or another flag are left alone.
		{"tool --token-file /run/t", "tool --token-file /run/t"},
		{"tool --secret-name db", "tool --secret-name db"},
		{"tool --token --verbose", "tool --token --verbose"},
	} {
		if got := All(c.in); got != c.want {
			t.Errorf("All(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := All(c.want); got != c.want {
			t.Errorf("All is not idempotent on %q: %q", c.want, got)
		}
	}
	if got := Literal("args := []string{\"--token\", tok}\n"); strings.Contains(got, Mark) {
		t.Errorf("Literal masked source code: %q", got)
	}
}

// Swarm message bodies stored before the link ingest masked them are
// rescrubbed: link_messages.body, through All.
func TestRescrubCoversLinkMessages(t *testing.T) {
	if cols, ok := Targets()["link_messages"]; !ok || strings.Join(cols, ",") != "body,reason" {
		t.Fatalf("Targets()[link_messages] = %v, want [body reason]", cols)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "msgs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clean := "ping from A"
	for i, body := range []string{"use this: " + redacttest.Joined(), clean} {
		if _, err := db.Exec(`INSERT INTO link_messages (kind, from_session, to_address, body, truncated, at)
			VALUES ('send', 'u-a', 'u-b', ?, 0, ?)`, body, fmt.Sprintf("2026-10-22T00:00:0%d.000Z", i)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := CountStored(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if before.Columns["link_messages.body"]["github_token"] != 1 {
		t.Errorf("count before = %v, want 1 github_token", before.Columns["link_messages.body"])
	}
	stats, err := Rescrub(context.Background(), db, fastCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Done["link_messages"] || stats.Changed["link_messages"] != 1 {
		t.Errorf("link_messages: done=%v changed=%d, want done and 1", stats.Done["link_messages"], stats.Changed["link_messages"])
	}
	for _, l := range leaks(t, db) {
		t.Error(l)
	}
	var got string
	if err := db.QueryRow(`SELECT body FROM link_messages ORDER BY id DESC LIMIT 1`).Scan(&got); err != nil || got != clean {
		t.Errorf("clean body = %q (%v)", got, err)
	}
}
