package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// cmdRescrub is `swarmery rescrub --count --db <path>`: per-class counts of the
// secret-shaped values the background rescrub would mask, never the values.
// It only reads (no migration) and refuses the default live store unless
// --allow-default-db is given: measure on a copy.
func cmdRescrub(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("rescrub", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dbPath := fs.String("db", "", "path to a COPY of the SQLite database (required)")
	count := fs.Bool("count", false, "print per-class counts of values a rescrub would mask")
	allowDefault := fs.Bool("allow-default-db", false, "allow counting the default ~/.swarmery store in place")
	if err := fs.Parse(args); err != nil {
		return usageError{"rescrub: " + err.Error()}
	}
	if !*count || *dbPath == "" {
		return usageError{"usage: swarmery rescrub --count --db <path-to-a-copy>  (the rescrub itself runs inside `swarmery serve`)"}
	}
	if def, err := store.DefaultDBPath(); err == nil && !*allowDefault {
		if resolvePath(*dbPath) == resolvePath(def) {
			return usageError{"rescrub: refusing the default store " + def + " — count a copy instead"}
		}
	}
	// Read-only pool (query_only): counting never writes or migrates.
	db, err := store.OpenReader(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	c, err := redact.CountStored(context.Background(), db)
	if err != nil {
		return err
	}
	for _, l := range c.Lines() {
		fmt.Fprintln(out, l)
	}
	return nil
}

// resolvePath is p made absolute with every symlink resolved, so a link to the
// live store is recognised as the live store. A path that cannot be resolved
// (missing) falls back to its absolute form.
func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}
