package redact

// Key-cued masking: a JSON payload's object key names what its value is. A
// string under a key such as password, client_secret, api_key or
// GITHUB_TOKEN is a credential whatever its shape, so the whole string is
// masked (PayloadJSON / Value, and countPayload counts it as KeyCuedClass).
//
// The key is split into lower-case words (snake, kebab and camel case) and
// matched word by word, never as a substring, so the count fields every
// transcript carries — input_tokens, max_tokens, outputTokens — and words that
// merely start like a cue (author, authority, tokenizer) are not secret keys:
//   - password, passwd, passphrase or secret anywhere in the key;
//   - a key ENDING in token, auth, authorization, credential(s), apikey,
//     accesskey or privatekey, or in the word pairs api key, access key,
//     private key — so auth_url, token_count and private_key_path are not.
//
// Only string values are masked: numbers, booleans and null never are. The
// code fields of the file-editing tools (codeFields) are exempt, as they are
// from every contextual rule (decision D6): a key inside their text is
// source, and the walk does not decode it.

import (
	"strings"
	"unicode"
)

// KeyCuedClass is the count class of a value masked for its key's name.
const KeyCuedClass = "key_cued"

// secretKey reports whether a JSON object key names a credential.
func secretKey(k string) bool {
	words := keyWords(k)
	n := len(words)
	if n == 0 {
		return false
	}
	for _, w := range words {
		switch w {
		case "password", "passwd", "passphrase", "secret":
			return true
		}
	}
	switch words[n-1] {
	case "token", "auth", "authorization", "credential", "credentials", "apikey", "accesskey", "privatekey":
		return true
	case "key":
		if n >= 2 {
			switch words[n-2] {
			case "api", "access", "private":
				return true
			}
		}
	}
	return false
}

// keyWords splits a key into lower-case words: at every rune that is not a
// letter or digit, before an upper-case letter that follows a lower-case
// letter or digit (accessToken → access token), and before the last capital
// of an upper-case run followed by a lower-case letter (APIKey → api key).
func keyWords(k string) []string {
	rs := []rune(k)
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if unicode.IsUpper(r) && len(cur) > 0 {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return words
}

// keyCued returns the masked value for a string under a secret key, and
// whether it differs from s. An empty or already masked string is kept.
func keyCued(s string) (string, bool) {
	if s == "" || s == redactedMark {
		return s, false
	}
	return redactedMark, true
}
