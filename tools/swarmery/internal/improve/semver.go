package improve

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// bumpPatch increments the patch level of a strict MAJOR.MINOR.PATCH semver
// string (no pre-release / build metadata — the plugin manifests only ever
// carry plain triples). "2.2.0" → "2.2.1".
func bumpPatch(v string) (string, error) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("not a MAJOR.MINOR.PATCH version: %q", v)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || p == "" {
			return "", fmt.Errorf("non-numeric version component in %q", v)
		}
		nums[i] = n
	}
	return fmt.Sprintf("%d.%d.%d", nums[0], nums[1], nums[2]+1), nil
}

// versionFieldRe matches the first top-level `"version": "x.y.z"` field of a
// manifest, capturing the value so only it is rewritten — key order and
// formatting elsewhere survive byte-for-byte (a map round-trip would reorder
// keys and churn the diff).
var versionFieldRe = regexp.MustCompile(`("version"\s*:\s*")([^"]*)(")`)

// bumpVersionField rewrites the first "version" field of raw JSON to newVer,
// returning the modified bytes and the previous value. An absent version field
// is an error — the caller only calls this on manifests known to carry one.
func bumpVersionField(raw []byte, newVer string) ([]byte, string, error) {
	loc := versionFieldRe.FindSubmatchIndex(raw)
	if loc == nil {
		return nil, "", fmt.Errorf("no \"version\" field found")
	}
	// loc groups: [0:1]=whole, [2:3]=prefix (`"version": "`), [4:5]=value,
	// [6:7]=closing quote. Rewrite only the value span so the original
	// whitespace/formatting of prefix and suffix is preserved verbatim.
	old := string(raw[loc[4]:loc[5]])
	out := append([]byte{}, raw[:loc[4]]...)
	out = append(out, []byte(newVer)...)
	out = append(out, raw[loc[5]:]...)
	return out, old, nil
}
