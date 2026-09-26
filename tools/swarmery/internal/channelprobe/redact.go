package channelprobe

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxStringBytes caps every string in a result. The longest legitimate string is
// a CLI install path or a one-line note; a value longer than this is far more
// likely to be something that was echoed by mistake — a credential, a config
// blob — than a measurement.
const maxStringBytes = 200

// forbiddenKeys are key names a result may never carry, at any depth. The probe
// COUNTS names and records booleans; a key called "value" is the shape of a
// leak, whatever it happens to hold today.
var forbiddenKeys = map[string]bool{"value": true, "secret": true, "token": true}

// Validate refuses a result that could carry a value from the operator's
// environment. The invariant "count names, never echo values" is enforced by
// shape, not by the harness author's care: the result is written to disk and a
// later phase renders it in a dashboard.
//
// The walk runs over the result's JSON form, so a field added to Result later is
// covered without touching this function.
func Validate(r Result) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("channelprobe: encode for validation: %w", err)
	}
	return validateJSON(data)
}

// validateJSON applies the same rules to raw bytes. Parse runs it BEFORE
// decoding into a Result, because decoding silently drops keys Result does not
// declare — and a file carrying a "value" key must be refused, not quietly
// accepted minus the part that leaked.
func validateJSON(data []byte) error {
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return fmt.Errorf("channelprobe: decode for validation: %w", err)
	}
	return walk("$", tree)
}

func walk(at string, node any) error {
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic: the first offence reported is stable
		for _, k := range keys {
			if forbiddenKeys[strings.ToLower(k)] {
				return fmt.Errorf("channelprobe: refusing result: key %q at %s is the shape of a leaked value", k, at)
			}
			if len(k) > maxStringBytes {
				return fmt.Errorf("channelprobe: refusing result: a key at %s is %d bytes (max %d)", at, len(k), maxStringBytes)
			}
			if err := walk(at+"."+k, v[k]); err != nil {
				return err
			}
		}
	case []any:
		for i, e := range v {
			if err := walk(fmt.Sprintf("%s[%d]", at, i), e); err != nil {
				return err
			}
		}
	case string:
		if len(v) > maxStringBytes {
			// Never quote the string itself: it is the suspected leak.
			return fmt.Errorf("channelprobe: refusing result: string at %s is %d bytes (max %d)", at, len(v), maxStringBytes)
		}
	}
	return nil
}
