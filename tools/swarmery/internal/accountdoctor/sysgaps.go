package accountdoctor

// Arm (g) — two surfaces are still hardwired to the default account's
// ~/.claude, deliberately (widening sysscan changes the identity of every
// scanned component; pointing the Memory surface — which WRITES — at a second
// tree needs a decision about which account a memory file belongs to first).
// The doctor states the gap instead of leaving it to be discovered. Fixed
// findings, severity info; the two source files are not touched.

func (r *run) sysgaps() {
	r.add(Finding{ID: "sysscan-single-account", Severity: SevInfo,
		Title:  "the System Hub scans only the default account's config dir",
		Detail: "sysscan.DefaultClaudeDir() is ~/.claude; agents, skills and hooks installed only under another account are not scanned",
		File:   "tools/swarmery/internal/sysscan/sysscan.go:51"})
	r.add(Finding{ID: "memory-single-account", Severity: SevInfo,
		Title:  "the Memory surface reads only the default account's config dir",
		Detail: "defaultMemoryClaudeDir() is ~/.claude and its setter AttachMemoryDirs (memory.go:82) is not called in production; another account's project memory is not shown",
		File:   "tools/swarmery/internal/api/memory.go:69"})
}
