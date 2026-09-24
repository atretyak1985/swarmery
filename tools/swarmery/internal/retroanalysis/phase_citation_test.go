package retroanalysis

import (
	"strings"
	"testing"
)

// [E:phase:<id>] is part of the vocabulary (learning loop phase 13.6): an
// analysis citing a surprise the digest offered validates, and one citing a
// phase the digest never offered is a fabrication.
func TestPhaseCitationsValidate(t *testing.T) {
	digest := "## Forecast surprises\n\n- Plan / Phase — surprise 0.80 (top: outcome_miss): … [E:phase:12]\n"
	allowed := AllowedCitations(digest)
	if !allowed["phase:12"] {
		t.Fatalf("AllowedCitations(%q) = %v, want phase:12", digest, allowed)
	}
	md := "## Що болить\nФаза промахнулась [E:phase:12]\n\n## Чому\nПрогноз [E:phase:12]\n\n## Що я б змінив\nПлан.\n"
	n, err := Validate(md, allowed)
	if err != nil || n != 1 {
		t.Fatalf("Validate = (%d, %v), want 1 distinct citation", n, err)
	}
	_, err = Validate(strings.ReplaceAll(md, "phase:12", "phase:99"), allowed)
	if err == nil || !strings.Contains(err.Error(), "phase:99") {
		t.Errorf("an invented phase citation validated: %v", err)
	}
}
