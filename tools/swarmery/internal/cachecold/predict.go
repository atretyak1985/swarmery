package cachecold

import (
	"database/sql"
	"sort"
	"strings"
	"time"
)

// Cold is one live session whose prompt cache has expired: the next message
// re-writes Footprint tokens at the cache-write rate.
type Cold struct {
	SessionID   int64
	SessionUUID string
	// IdleMinutes is how long ago the session's last main-thread turn ended.
	IdleMinutes int
	Footprint   int64
	Kind        Kind
	// Since is when the cache expired: the last turn's end + the kind's TTL.
	Since time.Time
}

// Candidates returns the live sessions whose cache went cold, largest footprint
// first. A session qualifies when:
//
//   - it is live — status active / idle / awaiting_reply. The status ticker caps
//     a session whose process is alive at idle, so a terminal left open for
//     hours stays here while a session with no liveness signal closes after the
//     idle window; ended sessions (completed, a dead process) never fire;
//   - it is interactive — a headless run (entrypoint sdk-cli) has no operator to
//     continue from a handoff;
//   - it is not archived, hidden or pruned (the handoff candidate rules);
//   - its newest MAIN-THREAD turn is an assistant turn — a newer user turn means
//     the next request is already in flight, so there is nothing left to warn
//     about — whose footprint is at least cfg.MinFootprint and which ended more
//     than the account kind's TTL ago.
//
// Subagent turns are left out: they run on their own caches, and the main
// thread's context is what the next message re-sends.
func Candidates(db *sql.DB, now time.Time, cfg Config) ([]Cold, error) {
	rows, err := db.Query(`
		SELECT s.id, s.session_uuid, COALESCE(s.account, ''), t.role, t.ended_at, t.started_at,
		       COALESCE(t.tokens_in, 0), COALESCE(t.tokens_cache_read, 0), COALESCE(t.tokens_cache_write, 0)
		FROM sessions s
		JOIN projects p ON p.id = s.project_id
		JOIN turns t ON t.id = (SELECT id FROM turns
		                        WHERE session_id = s.id AND agent_name IS NULL
		                        ORDER BY seq DESC LIMIT 1)
		WHERE p.archived = 0
		  AND s.hidden = 0
		  AND s.pruned = 0
		  AND s.status IN ('active', 'idle', 'awaiting_reply')
		  AND COALESCE(s.proc_state, '') <> 'dead'
		  AND COALESCE(s.entrypoint, '') <> 'sdk-cli'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Cold
	for rows.Next() {
		var (
			c              Cold
			account, role  string
			ended, started sql.NullString
			tin, tcr, tcw  int64
		)
		if err := rows.Scan(&c.SessionID, &c.SessionUUID, &account, &role, &ended, &started,
			&tin, &tcr, &tcw); err != nil {
			return nil, err
		}
		if role != "assistant" {
			continue
		}
		c.Footprint = Footprint(Turn{TokensIn: tin, CacheRead: tcr, CacheWrite: tcw})
		if c.Footprint <= 0 || c.Footprint < cfg.MinFootprint {
			continue
		}
		last := parseTS(ended.String)
		if last.IsZero() {
			last = parseTS(started.String)
		}
		if last.IsZero() {
			continue
		}
		c.Kind = cfg.Kind(account)
		ttl := cfg.TTL(c.Kind)
		idle := now.Sub(last)
		if ttl <= 0 || idle <= ttl {
			continue
		}
		c.IdleMinutes = int(idle / time.Minute)
		c.Since = last.Add(ttl).UTC()
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Footprint DESC, session id ASC — the handoff candidate order, so the two
	// lists read alike.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Footprint != out[j].Footprint {
			return out[i].Footprint > out[j].Footprint
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out, nil
}

// parseTS reads a stored RFC 3339 timestamp (transcripts carry fractional
// seconds, daemon writes do not); zero on anything else.
func parseTS(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

// formatTS is the store's timestamp spelling for daemon-written columns.
func formatTS(t time.Time) string { return t.UTC().Format(time.RFC3339) }
