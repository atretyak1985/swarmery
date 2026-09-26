package ingest

import "encoding/json"

// record is the decoded superset of one JSONL line (docs/jsonl-format.md §2–§3).
type record struct {
	Type             string          `json:"type"`
	ParentUUID       *string         `json:"parentUuid"`
	IsSidechain      bool            `json:"isSidechain"`
	UUID             string          `json:"uuid"`
	Timestamp        string          `json:"timestamp"`
	CWD              string          `json:"cwd"`
	SessionID        string          `json:"sessionId"`
	Version          string          `json:"version"`
	GitBranch        string          `json:"gitBranch"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	PromptID         string          `json:"promptId"`
	PromptSource     string          `json:"promptSource"`
	AgentID          string          `json:"agentId"`
	Subtype          string          `json:"subtype"` // system records
	DurationMs       int64           `json:"durationMs"`
	AITitle          string          `json:"aiTitle"`          // ai-title records
	AttributionSkill string          `json:"attributionSkill"` // skill active on this assistant line (§9)
	Message          json.RawMessage `json:"message"`
	ToolUseResult    json.RawMessage `json:"toolUseResult"`
	Error            json.RawMessage `json:"error"` // system api_error

	raw []byte // the raw line, for unknown-type payloads and uuid-less hashing
}

// apiMessage is `message` on assistant records (raw Anthropic API message, §5)
// and, partially, on user records (§4).
type apiMessage struct {
	Model   string          `json:"model"`
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string (user prompt) or []contentBlock
	Usage   *usage          `json:"usage"`

	// StopReason is the API's own account of why the turn ended: end_turn,
	// tool_use, max_tokens, stop_sequence or refusal. It is duplicated across
	// the split lines of one message and is null on every line but the last,
	// so ingest only ever writes a NON-EMPTY value (migration 0078).
	//
	// `refusal` is the one that changes a decision: it is how an Opus 5.5
	// safeguard ends a turn, and the process still exits 0. Without it the
	// completion loop cannot tell a safeguard stop from a model that simply
	// finished talking.
	StopReason string `json:"stop_reason"`
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`

	// CacheCreation is the per-TTL breakdown of CacheCreationInputTokens
	// (docs/jsonl-format.md §6). The two TTLs bill at different rates — a 1h
	// write costs 2x input against 1.25x for a 5m write — so the flat total
	// alone cannot price a turn. Absent on older transcripts; see cacheWriteSplit.
	CacheCreation *cacheCreation `json:"cache_creation"`

	// Speed is the account speed mode of this message ("standard" / "fast").
	// Fast is a separate SKU at 2x standard rates, priced via the "<model>-fast"
	// row in config/pricing.json.
	Speed string `json:"speed"`
}

// cacheCreation is usage.cache_creation — cache writes split by TTL.
type cacheCreation struct {
	Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
}

// cacheWriteSplit returns this turn's cache writes as (5m, 1h).
//
// When the transcript carries no cache_creation object the whole flat
// cache_creation_input_tokens total is attributed to the 5m bucket. That is
// what every pre-split transcript was already priced at, so re-ingesting old
// history through this path reproduces its existing cost exactly.
func (u *usage) cacheWriteSplit() (fiveMin, oneHour int64) {
	if u == nil {
		return 0, 0
	}
	if u.CacheCreation == nil {
		return u.CacheCreationInputTokens, 0
	}
	fiveMin, oneHour = u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
	// A TTL bucket we do not know about yet (a future ephemeral_*_input_tokens
	// field) would otherwise disappear from the bill entirely. Attribute the
	// unaccounted remainder to 5m: the cheapest write rate, so the estimate
	// stays conservative rather than dropping the tokens on the floor.
	if rest := u.CacheCreationInputTokens - fiveMin - oneHour; rest > 0 {
		fiveMin += rest
	}
	return fiveMin, oneHour
}

// contentBlock is one element of assistant content (§5) or a user tool_result (§4b).
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	IsError   bool            `json:"is_error"`    // tool_result
	Text      string          `json:"text"`        // text (turn prose → turns.text)
}

// agentResult is toolUseResult of an Agent completion (§7). Background
// (run_in_background) launches carry isAsync + status "async_launched" on the
// immediate result and never report totalDurationMs.
type agentResult struct {
	Status          string          `json:"status"`
	IsAsync         bool            `json:"isAsync"`
	AgentID         string          `json:"agentId"`
	AgentType       string          `json:"agentType"`
	TotalDurationMs int64           `json:"totalDurationMs"`
	TotalTokens     int64           `json:"totalTokens"`
	ToolStats       json.RawMessage `json:"toolStats"`
}

// fileChangeResult is toolUseResult of Edit / Write (§8).
type fileChangeResult struct {
	Type            string      `json:"type"` // "create" on Write-create
	FilePath        string      `json:"filePath"`
	StructuredPatch []patchHunk `json:"structuredPatch"`
}

type patchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// sidechainMeta is agent-<id>.meta.json (§7).
type sidechainMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	ToolUseID   string `json:"toolUseId"`
}

// knownTypes are all record types catalogued in docs/jsonl-format.md §2.
// Anything else becomes an events row with type='unknown'.
var knownTypes = map[string]bool{
	"assistant":             true,
	"attachment":            true,
	"user":                  true,
	"last-prompt":           true,
	"mode":                  true,
	"ai-title":              true,
	"permission-mode":       true,
	"file-history-snapshot": true,
	"system":                true,
	"queue-operation":       true,
	"pr-link":               true,
	"bridge-session":        true,
	"agent-name":            true,
}
