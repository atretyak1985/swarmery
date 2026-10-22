package redact

import (
	"os"
	"strconv"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// TestWriteScratchDB builds a synthetic store for a manual rescrub check on an
// isolated daemon. It runs only when SWARMERY_SCRATCH_DB names the file to
// create (SWARMERY_SCRATCH_ROWS, default 50000, sets its size):
//
//	SWARMERY_SCRATCH_DB=$tmp/.swarmery/swarmery.db go test ./internal/redact -run TestWriteScratchDB
func TestWriteScratchDB(t *testing.T) {
	path := os.Getenv("SWARMERY_SCRATCH_DB")
	if path == "" {
		t.Skip("SWARMERY_SCRATCH_DB not set — manual scratch-store generator")
	}
	rows := 50000
	if v, err := strconv.Atoi(os.Getenv("SWARMERY_SCRATCH_ROWS")); err == nil && v > 0 {
		rows = v
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := redacttest.Seed(db, rows)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %s: %d rows, %d with secrets", path, st.Rows, st.SecretRows)
}
