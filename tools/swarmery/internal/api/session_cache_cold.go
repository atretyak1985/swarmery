package api

import "database/sql"

// sessionCacheColdCols is the «cache cold» tail of a session row (migration
// 0127), declared beside the scan that reads it (same convention as
// sessionModelCols). The daemon's 60 s tick (internal/cachecold.Sync) keeps one
// session_cache_cold row per session; an open row (cleared_at NULL) is the
// prediction that the next message re-writes the whole context. Three lookups
// on the table's primary key, so the busiest list in the product pays three
// index probes per row.
const sessionCacheColdCols = `,
	       (SELECT scc.since FROM session_cache_cold scc
	         WHERE scc.session_uuid = s.session_uuid AND scc.cleared_at IS NULL),
	       (SELECT scc.kind FROM session_cache_cold scc
	         WHERE scc.session_uuid = s.session_uuid AND scc.cleared_at IS NULL),
	       (SELECT scc.footprint FROM session_cache_cold scc
	         WHERE scc.session_uuid = s.session_uuid AND scc.cleared_at IS NULL)`

// sessionCacheColdDTO is sessionDTO.CacheCold: the session's prompt cache has
// expired and its next message re-writes Footprint tokens. The card shows a
// «cache cold» badge with a «continue from handoff» action.
type sessionCacheColdDTO struct {
	// Since is when the cache expired: the last turn's end + the TTL of Kind.
	Since string `json:"since"`
	// Kind is the billing channel the TTL came from: api (5 min) or
	// subscription (1 h).
	Kind      string `json:"kind"`
	Footprint int64  `json:"footprint"`
}

// sessionCacheColdScan holds that tail.
type sessionCacheColdScan struct {
	since, kind sql.NullString
	footprint   sql.NullInt64
}

func (c *sessionCacheColdScan) dest() []any { return []any{&c.since, &c.kind, &c.footprint} }

// dto is nil when the session has no open cache-cold row.
func (c *sessionCacheColdScan) dto() *sessionCacheColdDTO {
	if !c.since.Valid {
		return nil
	}
	return &sessionCacheColdDTO{Since: c.since.String, Kind: c.kind.String, Footprint: c.footprint.Int64}
}
