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
	AITitle          string          `json:"aiTitle"` // ai-title records
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
}

type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// contentBlock is one element of assistant content (§5) or a user tool_result (§4b).
type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	IsError   bool            `json:"is_error"`    // tool_result
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
