package claudeacct

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// The billing channels of an account. They decide how long the prompt cache
// lives (internal/cachecold): Claude Code writes a 1 h cache on a subscription
// and a 5 min one on an API key.
const (
	BillingSubscription = "subscription"
	BillingAPI          = "api"
)

// lookupEnv is the process-environment seam, so tests never read the real one.
var lookupEnv = os.Getenv

// BillingKind tells whether an account bills against an API key or a Claude
// subscription. key is a sessions.account value; "" is the default account.
//
// An account is BillingAPI when any API-key credential is configured for it:
//
//   - its config dir's settings.json sets env.ANTHROPIC_API_KEY or an
//     apiKeyHelper (the CLI then authenticates with the key, not the login);
//   - its profile .claude.json carries a primaryApiKey (a Console login) —
//     ~/.claude.json for the default account, <configDir>/.claude.json for the
//     others, the files the CLI itself reads;
//   - it is the account this daemon's own runs inherit (UnboundAccount) and
//     ANTHROPIC_API_KEY is set in the daemon's environment.
//
// Anything else — an OAuth login, a missing or unreadable config dir, a legacy
// row — is BillingSubscription: the default the cache-cold thresholds assume.
// Only the PRESENCE of a key is tested; no value is kept or returned.
func BillingKind(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		key = ingest.DefaultAccount
	}
	if key == ingest.UnboundAccount() && strings.TrimSpace(lookupEnv("ANTHROPIC_API_KEY")) != "" {
		return BillingAPI
	}
	dir, err := ConfigDirFor(key)
	if err != nil {
		return BillingSubscription
	}
	if settingsUseAPIKey(filepath.Join(dir, "settings.json")) {
		return BillingAPI
	}
	profile := filepath.Join(dir, ".claude.json")
	if key == ingest.DefaultAccount {
		if home, err := userHomeDir(); err == nil && home != "" {
			profile = filepath.Join(home, ".claude.json")
		}
	}
	if profileHasAPIKey(profile) {
		return BillingAPI
	}
	return BillingSubscription
}

// settingsUseAPIKey reports whether a settings.json authenticates by API key.
func settingsUseAPIKey(path string) bool {
	return cachedFileFlag(path, func(raw []byte) bool {
		var s struct {
			Env          map[string]any `json:"env"`
			APIKeyHelper string         `json:"apiKeyHelper"`
		}
		if json.Unmarshal(raw, &s) != nil {
			return false
		}
		if strings.TrimSpace(s.APIKeyHelper) != "" {
			return true
		}
		v, _ := s.Env["ANTHROPIC_API_KEY"].(string)
		return strings.TrimSpace(v) != ""
	})
}

// profileHasAPIKey reports whether a .claude.json profile holds a Console key.
func profileHasAPIKey(path string) bool {
	return cachedFileFlag(path, func(raw []byte) bool {
		var p struct {
			PrimaryAPIKey string `json:"primaryApiKey"`
		}
		return json.Unmarshal(raw, &p) == nil && strings.TrimSpace(p.PrimaryAPIKey) != ""
	})
}

// fileFlag is one memoized verdict over a file, valid while its size and mtime
// hold. A profile .claude.json can run to megabytes and the cache-cold tick asks
// every minute, so a file is re-parsed only after it changed.
type fileFlag struct {
	size  int64
	mtime time.Time
	flag  bool
}

var (
	fileFlagsMu sync.Mutex
	fileFlags   = map[string]fileFlag{}
)

// cachedFileFlag runs parse over path's bytes, or returns the remembered answer
// for an unchanged file. A missing or unreadable file is false.
func cachedFileFlag(path string, parse func([]byte) bool) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	fileFlagsMu.Lock()
	c, ok := fileFlags[path]
	fileFlagsMu.Unlock()
	if ok && c.size == fi.Size() && c.mtime.Equal(fi.ModTime()) {
		return c.flag
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	flag := parse(raw)
	fileFlagsMu.Lock()
	fileFlags[path] = fileFlag{size: fi.Size(), mtime: fi.ModTime(), flag: flag}
	fileFlagsMu.Unlock()
	return flag
}
