package accountdoctor

// Arm (i) — the trust findings (D5 Lock 1 and Lock 2, D9), paths and reasons
// only, never a store's or a settings file's contents:
//
//	estate-unanchored        warn   Fast  the estate store is absent, carries no
//	                                      root line, or its roots do not admit
//	                                      the estate root
//	store-rootless           warn   Fast  an ACCOUNT store with no root line,
//	                                      released after Lock 1 (today's rule)
//	estate-settings-unusable error  Fast  <EstateRoot>/.claude/settings.json is
//	                                      one the composer refuses (D9)
//	binding-tracked          warn   Full  a binding under the estate that Lock 1
//	                                      ignores (tracked, or indeterminate)
//	estate-settings-tracked  warn   Full  <EstateRoot>/.claude/settings.json is
//	                                      git-tracked
//
// Root lines are read by claudeacct through the secrets loader's own
// no-follow / fstat / 0700-dir discipline; a store that discipline refuses
// counts as rootless. The two Full findings cost further git calls, so Fast —
// the SessionStart path — never computes them.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runsettings"
)

// trust is the Fast half.
func (r *run) trust() {
	res := r.res
	if res.Estate != "" && !res.EstateAdmitted {
		store := claudeacct.SecretsPath(res.Estate)
		r.add(Finding{ID: "estate-unanchored", Severity: SevWarn,
			Title:  fmt.Sprintf("estate %s is unanchored: it releases no credentials and no estate settings", res.Estate),
			Detail: unanchoredReason(res, store),
			File:   store})
	}
	if key := strings.TrimSpace(res.Account); key != "" && key != ingest.DefaultAccount && res.AccountRoot != "" &&
		res.AccountStoreAdmitted {
		if rooted, present, _ := claudeacct.StoreAdmits(key, res.AccountRoot); present && !rooted {
			store := claudeacct.SecretsPath(key)
			r.add(Finding{ID: "store-rootless", Severity: SevWarn,
				Title: fmt.Sprintf("the account store %s.env carries no root line", key),
				Detail: fmt.Sprintf("%s is released to every directory bound to %s; to confine it, add: %s",
					store, key, claudeacct.RootLineFor(res.AccountRoot)),
				File: store})
		}
	}
	if why, file := estateSettingsUnusable(res); why != "" {
		r.add(Finding{ID: "estate-settings-unusable", Severity: SevError,
			Title:  "the estate's settings file is unusable, so launches fall back to the lent settings",
			Detail: fmt.Sprintf("%s: %s", file, why),
			File:   file})
	}
}

// unanchoredReason says which of the three unanchored states the estate is in.
func unanchoredReason(res claudeacct.Resolution, store string) string {
	state, why := claudeacct.StoreStatus(store)
	switch state {
	case claudeacct.StoreAbsent:
		return fmt.Sprintf("no store at %s; a credential-free estate is an EMPTY store carrying: %s",
			store, claudeacct.RootLineFor(res.EstateRoot))
	case claudeacct.StoreRefused:
		return fmt.Sprintf("%s is refused by the loader (%s), so it counts as rootless", store, why)
	}
	rooted, admitted, add := claudeacct.StoreAdmits(res.Estate, res.EstateRoot)
	switch {
	case !rooted:
		return fmt.Sprintf("%s carries no root line; an estate store must be rooted — add: %s",
			store, claudeacct.RootLineFor(res.EstateRoot))
	case !admitted:
		return fmt.Sprintf("the roots in %s do not admit %s — add: %s", store, res.EstateRoot, add)
	}
	return fmt.Sprintf("the roots in %s admit %s but not this path's physical location %s",
		store, res.EstateRoot, resolved(res.EstateRoot))
}

// estateSettingsUnusable is D9's list for <EstateRoot>/.claude/settings.json:
// the trusted loader's refusal (malformed, not an object, too large, not a
// regular file, not owned, group/other-writable, hard-linked, outside the
// root, reached through an absolute link) or an EstateKey of the wrong JSON
// type. "" when the file is absent or usable.
func estateSettingsUnusable(res claudeacct.Resolution) (why, file string) {
	if res.EstateRoot == "" {
		return "", ""
	}
	file = filepath.Join(res.EstateRoot, filepath.FromSlash(claudeacct.ProjectSettingsFile))
	if _, err := os.Lstat(file); err != nil {
		return "", file
	}
	root, reason := claudeacct.ReadTrustedSettingsWithin(file, res.EstateRoot)
	if reason != "" {
		return reason, file
	}
	if root == nil {
		return "", file
	}
	for _, k := range runsettings.EstateKeys {
		if v, ok := root[k]; ok {
			if _, isObject := v.(map[string]any); !isObject {
				return "wrong-type:" + k, file
			}
		}
	}
	return "", file
}

// fullTrust is the Full-only half: the findings that need further git calls.
func (r *run) fullTrust() {
	res := r.res
	if res.EstateRoot == "" {
		return
	}
	for _, ig := range res.IgnoredRungs() {
		if within(res.EstateRoot, filepath.Dir(filepath.Dir(ig.Path))) {
			r.add(Finding{ID: "binding-tracked", Severity: SevWarn,
				Title: "a binding under the estate is ignored by the provenance gate", Detail: ig.Path + " — " + ig.Reason,
				File: ig.Path})
		}
	}
	for _, e := range claudeacct.ScanPinsDetail(res.EstateRoot) {
		// Key set AND Ignored set is exactly Lock 1's verdict: the trusted loader
		// read the pin, and the provenance probe refused it.
		if e.Key != "" && e.Ignored != "" {
			file := filepath.Join(e.Dir, filepath.FromSlash(claudeacct.BindingFile))
			r.add(Finding{ID: "binding-tracked", Severity: SevWarn,
				Title: "a binding under the estate is ignored by the provenance gate", Detail: e.Ignored, File: file})
		}
	}
	if why := claudeacct.EstateSettingsTracked(res.EstateRoot); why != "" {
		file := filepath.Join(res.EstateRoot, filepath.FromSlash(claudeacct.ProjectSettingsFile))
		r.add(Finding{ID: "estate-settings-tracked", Severity: SevWarn,
			Title: "the estate's settings file is git-tracked", Detail: why, File: file})
	}
}
