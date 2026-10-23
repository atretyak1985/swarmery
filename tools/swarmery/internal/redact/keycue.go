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
//     accesskey, privatekey, cookie or pass, in pwd after another word
//     (db_pwd; a bare PWD is the working directory), or in "<noun> key" for
//     api, access, private, session, signing, encryption, master and client —
//     so auth_url, token_count, private_key_path, passed and cache_key are
//     not;
//   - a key ending in a plural (secrets, passwords, tokens, cookies, apiKeys,
//     signing_keys, …) masks only a string that is not a number, so a count
//     such as input_tokens stays visible as a number or a numeric string.
//
// A string array under such a key is masked element by element. Only string
// values are masked: numbers, booleans and null never are. The
// code fields of the file-editing tools (codeFields) are exempt, as they are
// from every contextual rule (decision D6): a key inside their text is
// source, and the walk does not decode it.

import (
	"strings"
	"unicode"
)

// KeyCuedClass is the count class of a value masked for its key's name.
const KeyCuedClass = "key_cued"

// cue is what a key says about its value.
type cue int

const (
	noCue cue = iota
	// cueSecret: any string value is a credential.
	cueSecret
	// cuePlural: a plural cue word (secrets, tokens, apiKeys, …) names a
	// credential only when the string is not a number — input_tokens: "1234"
	// is a count.
	cuePlural
)

// keyNouns are the words that make "<noun> key" a secret (api_key,
// signing_key, MASTER_KEY); cache_key, sort_key or primary_key are not.
var keyNouns = map[string]bool{
	"api": true, "access": true, "private": true, "session": true, "signing": true,
	"encryption": true, "master": true, "client": true,
}

// keyCue classifies a JSON object key.
func keyCue(k string) cue {
	words := keyWords(k)
	n := len(words)
	if n == 0 {
		return noCue
	}
	for _, w := range words {
		switch w {
		case "password", "passwd", "passphrase", "secret":
			return cueSecret
		}
	}
	prev := ""
	if n >= 2 {
		prev = words[n-2]
	}
	switch last := words[n-1]; last {
	case "token", "auth", "authorization", "credential", "credentials", "apikey", "accesskey", "privatekey",
		"cookie", "pass":
		return cueSecret
	case "pwd":
		// A bare PWD is the shell's working directory; db_pwd, MYSQL_PWD are
		// passwords.
		if n >= 2 {
			return cueSecret
		}
	case "key":
		if keyNouns[prev] {
			return cueSecret
		}
	case "secrets", "passwords", "passphrases", "tokens", "cookies", "apikeys":
		return cuePlural
	case "keys":
		if keyNouns[prev] {
			return cuePlural
		}
	}
	return noCue
}

// secretKey reports whether a JSON object key names a credential whatever the
// string under it holds.
func secretKey(k string) bool { return keyCue(k) == cueSecret }

// maskCued returns s masked for a key of cue c, and whether it changed.
func maskCued(c cue, s string) (string, bool) {
	if c == cuePlural && numeric(s) {
		return s, false
	}
	return keyCued(s)
}

// numeric reports whether s is a number as a count is written: digits with an
// optional sign, decimal point and thousands separators.
func numeric(s string) bool {
	s = strings.TrimSpace(s)
	digits := 0
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.' || r == ',' || r == '_':
		case (r == '-' || r == '+') && i == 0:
		default:
			return false
		}
	}
	return digits > 0
}

// cuedValue masks the value under a key of cue c: a string whole, and each
// string of an array. Other array elements go through rec, the walk's own
// redactor. ok is false for any other value (an object, a number), which the
// walk then handles as usual.
func cuedValue(c cue, v any, rec func(any) (any, bool)) (out any, changed, ok bool) {
	switch x := v.(type) {
	case string:
		r, ch := maskCued(c, x)
		return r, ch, true
	case []string:
		var o []string
		for i, e := range x {
			r, ch := maskCued(c, e)
			if !ch {
				continue
			}
			if o == nil {
				o = append([]string(nil), x...)
			}
			o[i] = r
		}
		if o == nil {
			return x, false, true
		}
		return o, true, true
	case []any:
		var o []any
		for i, e := range x {
			var r any
			var ch bool
			if s, isStr := e.(string); isStr {
				r, ch = maskCued(c, s)
			} else {
				r, ch = rec(e)
			}
			if !ch {
				continue
			}
			if o == nil {
				o = append([]any(nil), x...)
			}
			o[i] = r
		}
		if o == nil {
			return x, false, true
		}
		return o, true, true
	}
	return v, false, false
}

// countCued counts, into acc, what cuedValue would mask under a key of cue c;
// rec counts a non-string array element. ok as for cuedValue.
func countCued(c cue, v any, acc map[string]int, rec func(any)) bool {
	switch x := v.(type) {
	case string:
		if _, ch := maskCued(c, x); ch {
			acc[KeyCuedClass]++
		}
		return true
	case []any:
		for _, e := range x {
			if s, isStr := e.(string); isStr {
				if _, ch := maskCued(c, s); ch {
					acc[KeyCuedClass]++
				}
				continue
			}
			rec(e)
		}
		return true
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
