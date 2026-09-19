package main

import (
	"fmt"
	"io"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/version"
)

// versionAliases are the spellings that print the build identity. Three on
// purpose: `version` matches the subcommand grammar of every other verb,
// `--version` is what every other CLI answers, and `-v` is what people type
// first when a binary will not say what it is.
var versionAliases = []string{"version", "--version", "-v"}

// isVersionCommand reports whether arg is one of versionAliases.
func isVersionCommand(arg string) bool {
	for _, a := range versionAliases {
		if arg == a {
			return true
		}
	}
	return false
}

// cmdVersion prints the build identity — ONE line, nothing else. Until now the
// only way to read it was GET /api/health against a RUNNING daemon, so a
// freshly downloaded binary, or one being named in a bug report, could not say
// what it was. One line because this is what gets pasted into issue reports; a
// banner would be noise around the only value that matters.
func cmdVersion(w io.Writer) {
	fmt.Fprintln(w, version.String())
}
