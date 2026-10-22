package ingest

import (
	"database/sql"
	"log"
	"sync"
	"sync/atomic"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/cachecold"
)

// cacheColdCfg is the cachecold config the tail applies to every new
// main-thread assistant turn. The daemon injects it once at startup
// (SetCacheCold) with the real account-kind resolver — claudeacct imports this
// package, so the resolver cannot be called from here directly. Until then the
// shipped guards defaults apply, with every account read as a subscription.
var cacheColdCfg atomic.Pointer[cachecold.Config]

var cacheColdDefault = sync.OnceValue(func() cachecold.Config {
	c, err := cachecold.DefaultConfig()
	if err != nil {
		// A broken embedded guards.json: a zero TTL marks nothing, which is the
		// safe failure for a cost annotation.
		log.Printf("warn: ingest: cache-cold defaults: %v", err)
	}
	return c
})

// SetCacheCold installs the cache-cold config ingest evaluates turns with.
func SetCacheCold(cfg cachecold.Config) { cacheColdCfg.Store(&cfg) }

func cacheColdConfig() cachecold.Config {
	if c := cacheColdCfg.Load(); c != nil {
		return *c
	}
	return cacheColdDefault()
}

// markColdRestart sets turns.cold_restart on a just-inserted main-thread
// assistant turn when it paid for a cold cache against the previous REAL
// main-thread assistant turn of the session (cachecold.IsColdRestart; a
// zero-usage `<synthetic>` stub in between refreshed no cache and is skipped). It runs inside the
// tail transaction, so the mark commits — or rolls back — with the turn itself.
// Cost on the hot path: one indexed lookup of the predecessor per NEW assistant
// turn; replays never get here (upsertTurn reports them as not created).
func (in *ingester) markColdRestart(turnID int64, seq int, ts string, u *usage) error {
	if u == nil {
		return nil
	}
	var (
		started, ended string
		tin, tcr, tcw  int64
		w5, w1h        sql.NullInt64
	)
	err := in.tx.QueryRow(`
		SELECT started_at, COALESCE(ended_at, started_at),
		       COALESCE(tokens_in, 0), COALESCE(tokens_cache_read, 0), COALESCE(tokens_cache_write, 0),
		       cache_write_5m_tokens, cache_write_1h_tokens
		FROM turns
		WHERE session_id = ? AND seq < ? AND role = 'assistant' AND agent_name IS NULL
		  AND `+cachecold.RealTurnSQL+`
		ORDER BY seq DESC LIMIT 1`, in.sessionID, seq).
		Scan(&started, &ended, &tin, &tcr, &tcw, &w5, &w1h)
	if err == sql.ErrNoRows {
		return nil // the session's first assistant turn
	}
	if err != nil {
		return err
	}
	prev := cachecold.Turn{
		StartedAt: parseTS(started), EndedAt: parseTS(ended),
		TokensIn: tin, CacheRead: tcr, CacheWrite: tcw,
		CacheWrite5m: w5.Int64, CacheWrite1h: w1h.Int64, Split: w5.Valid || w1h.Valid,
	}
	w5m, w1hr := u.cacheWriteSplit()
	cur := cachecold.Turn{
		StartedAt: parseTS(ts), EndedAt: parseTS(ts),
		TokensIn: u.InputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens,
		CacheWrite5m: w5m, CacheWrite1h: w1hr, Split: true,
	}
	cfg := cacheColdConfig()
	if !cachecold.IsColdRestart(prev, cur, cfg.Rule(in.cacheColdKind(cfg))) {
		return nil
	}
	_, err = in.tx.Exec(`UPDATE turns SET cold_restart = 1 WHERE id = ?`, turnID)
	return err
}

// cacheColdKind is the billing channel of this ingester's session, resolved
// once per run from sessions.account.
func (in *ingester) cacheColdKind(cfg cachecold.Config) cachecold.Kind {
	if in.coldKind != "" {
		return in.coldKind
	}
	var account string
	_ = in.tx.QueryRow(`SELECT COALESCE(account, '') FROM sessions WHERE id = ?`, in.sessionID).Scan(&account)
	in.coldKind = cfg.Kind(account)
	return in.coldKind
}

// clearCacheCold closes the session's open «cache cold» prediction: a new
// main-thread turn arrived, so the warning came true or was averted. The daemon
// tick would clear it within a minute too; doing it here makes the badge go in
// the same session_updated frame as the turn that ended it.
func (in *ingester) clearCacheCold(ts string) error {
	if in.sessionUUID == "" {
		return nil
	}
	_, err := in.tx.Exec(
		`UPDATE session_cache_cold SET cleared_at = ? WHERE session_uuid = ? AND cleared_at IS NULL`,
		ts, in.sessionUUID)
	return err
}
