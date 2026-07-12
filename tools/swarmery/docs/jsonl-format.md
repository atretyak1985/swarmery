# Claude Code JSONL transcript format — observed spec

**Status:** spike result (Step 02). Everything below is evidence from real transcripts on this
machine — nothing is taken from memory or third-party docs. Where something was *not* observed,
it says so explicitly (see [Open questions](#open-questions)).

**Evidence base:**

- 115 `.jsonl` files across 13 project dirs in `~/.claude/projects/`, Claude Code versions
  `2.1.111` … `2.1.197` (primary deep-dive files are `2.1.170`).
- Sessions examined in depth:
  | Character | File |
  |---|---|
  | Subagent (Agent tool) session + sidechain files | `-Volumes-Work-swarmery/9f22596e-4bb2-44f1-93ad-e8b84f17fa22.jsonl` (+ its `subagents/`, `tool-results/` dirs) |
  | Tool-heavy (41 Edit, 27 Bash, 13 Agent) | `-Volumes-Work-bloomblum/948a823d-95e0-4b0c-9a3a-126647195faa.jsonl` |
  | Long multi-prompt interactive | `-Volumes-Work-Skygor/7f4fbd6b-9f33-44c6-b0c7-f91c5dac8258.jsonl` |
  | Short simple (66 lines) | `-Volumes-Work-swarmery/2019f909-9db3-4f7b-8639-98bd5e9e38e9.jsonl` |
- Corpus-wide scans (record-type / tool-name / attachment-type frequency) over all 115 files.

Anonymized fixtures mirroring these sessions: [`testdata/fixtures/`](../testdata/fixtures/):

- `subagent-session.jsonl` — Skill invocation + Agent (subagent) dispatch/completion chain,
  with its sidechain companion `subagent-session/subagents/agent-ab12cd34ef56ab78d.jsonl`
  (+ `.meta.json`) mirroring the real on-disk layout (§1, §7).
- `tool-heavy-session.jsonl` — Read/Edit/Write/Bash chains incl. a failing Bash
  (`is_error: true`) and `structuredPatch` file-change results (§8).
- `simple-session.jsonl` — short two-prompt Q&A session with checkpoint records.

---

## 1. On-disk layout

```
~/.claude/projects/<slug>/                  # slug = cwd with '/' → '-', e.g. -Volumes-Work-bloomblum
  <sessionId>.jsonl                         # main transcript; sessionId is a UUID = file name
  <sessionId>/                              # OPTIONAL companion dir (same name, no extension)
    subagents/
      agent-<agentId>.jsonl                 # one sidechain transcript per subagent (agentId = 17-hex-char id)
      agent-<agentId>.meta.json             # {"agentType","description","toolUseId"}
    tool-results/
      <shortId>.txt                         # spill files for oversized tool outputs (referenced by absolute path)
  memory/                                   # (project memory .md files — not transcript data)
```

Key consequence for the parser: **subagent transcripts are separate files**, not inline
records. In the entire corpus (v2.1.111+) `isSidechain` is `false` on every line of every
main transcript; `isSidechain: true` appears only inside `subagents/agent-*.jsonl`.

---

## 2. Record types (top-level `type` field)

Corpus-wide counts (115 files):

| `type` | count | Envelope? | Purpose |
|---|---|---|---|
| `assistant` | 25 787 | yes | One content block of an API assistant message |
| `attachment` | 22 454 | yes | Context injected around user turns (hooks, listings, reminders) |
| `user` | 12 901 | yes | Typed/queued prompt **or** tool_result carrier |
| `last-prompt` | 4 220 | no | Checkpoint: last user prompt text + `leafUuid` pointer |
| `mode` | 4 157 | no | UI mode checkpoint (`"normal"`, …) |
| `ai-title` | 4 101 | no | AI-generated session title (repeated) |
| `permission-mode` | 3 937 | no | Permission mode checkpoint (`"auto"`, …) |
| `file-history-snapshot` | 2 252 | no | Checkpoint/rewind bookkeeping (`trackedFileBackups`) |
| `system` | 1 515 | yes | Runtime events, discriminated by `subtype` (see §9) |
| `queue-operation` | 1 143 | no | Prompt/notification queue ops: `enqueue` / `dequeue` / `remove` |
| `pr-link` | 293 | no | PR created in session: `prNumber`, `prUrl`, `prRepository` |
| `bridge-session` | 343 | no | Link to a remote bridge session (`bridgeSessionId: "cse_…"`) |
| `agent-name` | 88 | no | Assigned agent name for the session |

“Envelope” = carries the common conversation-record fields of §3. Non-envelope records are
small session-state checkpoints keyed only by `sessionId` (plus their own fields).
`last-prompt`/`mode`/`permission-mode`/`ai-title` repeat many times per file — they are
re-emitted state snapshots, not events (see Open questions).

Record types **not observed** anywhere in the corpus: `summary`, `progress` (both mentioned in
community reverse-engineering docs — presumably pre-2.1 format).

---

## 3. Common envelope (conversation records)

Every `user` / `assistant` / `attachment` / `system` line carries:

```json
{
  "parentUuid": "….uuid of parent record or null",
  "isSidechain": false,
  "uuid": "record's own UUID",
  "timestamp": "2026-07-12T11:12:51.419Z",
  "userType": "external",
  "entrypoint": "cli",              // observed: "cli" (62585), "claude-desktop" (72)
  "cwd": "/Volumes/Work/<project>",
  "sessionId": "<same UUID as the file name>",
  "version": "2.1.170",             // Claude Code version
  "gitBranch": "main"
}
```

Optional envelope fields observed: `isMeta` (true on system-injected user content),
`slug`, `logicalParentUuid` (only on `compact_boundary`), `promptId` (user records only —
groups everything belonging to one submitted prompt), `agentId` (sidechain files only).

### Session-level fields

There is **no session header record**. `cwd`, `gitBranch`, `version`, `sessionId` are
repeated on every envelope record; the session UUID also equals the file name. `gitBranch`
can change between lines (mid-session branch switch). Model is **not** session-level — it
lives in each assistant message (§6).

---

## 4. `user` records

Two distinct shapes, discriminated by `message.content`:

**(a) Real prompt** — `content` is a **string**:

```json
{"type":"user", "promptId":"b1785c49-…", "promptSource":"typed", "permissionMode":"auto",
 "message":{"role":"user","content":"<the prompt text>"}, …envelope…}
```

`promptSource` observed values: `typed` (926), `system` (91), `queued` (62), `sdk` (2).

**(b) Tool result carrier** — `content` is an **array** of `tool_result` blocks:

```json
{"type":"user",
 "message":{"role":"user","content":[
   {"type":"tool_result","tool_use_id":"toolu_01Az…","content":"Exit code 1\n…","is_error":true}]},
 "toolUseResult": "Error: Exit code 1\n…",
 "sourceToolAssistantUUID":"<uuid of the assistant line holding the tool_use>", …envelope…}
```

- `message.content[].tool_result` is what the model saw (string, or array of
  `{"type":"text",…}` blocks).
- `toolUseResult` (top level, present on 51/58 user lines in one sample) is the CLI's
  **structured** result — a string for errors, a tool-specific object otherwise (§8, §7).
- `sourceToolAssistantUUID` points back to the assistant record that issued the `tool_use`.

Other observed flags on user records: `isMeta: true` (injected content, e.g. skill bodies),
`sourceToolUseID` (the `toolu_…` id that caused the injection), `isCompactSummary: true` +
`isVisibleInTranscriptOnly: true` (continuation summary after context compaction — the
content string starts with "This session is being continued from a previous conversation…"),
`interruptedMessageId`.

---

## 5. `assistant` records — and the split-message trap

`message` is a raw Anthropic API Message:

```json
{"type":"assistant", "requestId":"req_011Ccx…",
 "message":{
   "model":"claude-fable-5",
   "id":"msg_011Ccx4j2roukp5FZUVskjJu",
   "type":"message","role":"assistant",
   "content":[ …exactly ONE block per JSONL line… ],
   "stop_reason":"tool_use",          // observed: tool_use, end_turn, null
   "stop_sequence":null, "stop_details":{…}, "diagnostics":null,
   "usage":{…see §6…}
 }, …envelope…}
```

**One API response is split across N consecutive JSONL lines — one content block each —
all sharing the same `message.id` and `requestId`, each line with its own `uuid` and
chained via `parentUuid`.** Observed content block types: `thinking`
(`{type,thinking,signature}`), `text`, `tool_use`. Example: message
`msg_011Ccx4j2rou…` = 4 lines: thinking, thinking, tool_use, tool_use.

`usage` is **duplicated verbatim on every split line** → token accounting MUST dedupe by
`message.id` (see mapping §11).

Optional fields: `isApiErrorMessage: true` (synthetic assistant line for a failed request),
`attributionSkill` / `attributionPlugin` (e.g. `"superpowers:executing-plans"` /
`"superpowers"`) — present on assistant lines produced while a skill was active. Also
observed: `attributionAgent` (sidechain assistant lines — the subagent type, e.g.
`"Explore"`) and `attributionMcpServer` / `attributionMcpTool` (assistant lines following
an MCP tool call).

### `tool_use` block

```json
{"type":"tool_use","id":"toolu_01UadEaYHcDCsSrvnT5kfghz","name":"Bash",
 "input":{ …tool-specific arguments… },
 "caller":{"type":"direct"}}
```

Tool name = `name`, arguments = `input` (already-parsed JSON object). `caller` is a CLI-side
annotation (observed value `{"type":"direct"}`), not part of the API block. Corpus-wide top tools:
`Bash` 5134, `Edit` 2488, `Read` 1936, `Write` 611, `Agent` 338, `TaskUpdate` 253,
`TaskCreate` 153, `AskUserQuestion` 126, `ToolSearch` 109, `Skill` 60, plus `mcp__<server>__<tool>`
names for MCP tools. **`Task` and `MultiEdit` were never observed** (0/115 files) — in these
versions the subagent tool is named `Agent` and multi-edits are sequential `Edit` calls.

### `tool_result` linking

The result arrives on the **next** `user` record whose `tool_result.tool_use_id` equals the
`tool_use.id` (§4b). Oversized results are spilled to disk and replaced by a marker:

```
"content": "<persisted-output>\nOutput too large (34.2KB). Full output saved to:
/Users/…/projects/<slug>/<sessionId>/tool-results/bvnmu1id3.txt\n\nPreview (first 2KB):\n…"
```

---

## 6. Usage tokens and model

Location: `assistant` line → `message.model` and `message.usage`:

```json
"usage": {
  "input_tokens": 17538,
  "cache_creation_input_tokens": 6935,
  "cache_read_input_tokens": 15457,
  "output_tokens": 621,
  "server_tool_use": {"web_search_requests": 0, "web_fetch_requests": 0},
  "service_tier": "standard",
  "cache_creation": {"ephemeral_1h_input_tokens": 6935, "ephemeral_5m_input_tokens": 0},
  "inference_geo": "not_available",
  "iterations": [ {…same numbers, "type":"message"…} ],
  "speed": "standard"
}
```

- cache write = `cache_creation_input_tokens`; cache read = `cache_read_input_tokens`.
- Model can differ per message and per subagent (main session `claude-fable-5`,
  an `Explore` subagent ran `claude-haiku-4-5-20251001`).
- Aggregated subagent usage additionally appears in the parent's Agent-completion
  `toolUseResult.usage` + `totalTokens` (§7) — do not double-count against sidechain files.

---

## 7. Subagents (Agent tool, sidechain files)

**Invocation** — assistant `tool_use` with `name: "Agent"`:

```json
{"type":"tool_use","id":"toolu_01FKEw…","name":"Agent",
 "input":{"description":"Explore skills","subagent_type":"Explore","prompt":"…full task prompt…"}}
```

`subagent_type` observed: `general-purpose`, `Explore` (matches the agent registry from the
`agent_listing_delta` attachment: claude, claude-code-guide, Explore, general-purpose, Plan,
statusline-setup + project agents).

**Execution** — a separate file `<sessionId>/subagents/agent-<agentId>.jsonl`:
- every line has `isSidechain: true`, `agentId: "<17-hex>"`, and `sessionId` = **parent**
  session id; first record is a `user` line with `parentUuid: null` whose string content =
  the `prompt` from `input` (uuid chains restart per sidechain file);
- `agent-<agentId>.meta.json` = `{"agentType":"general-purpose","description":"…","toolUseId":"toolu_01UadEaY…"}` —
  the join key back to the parent's `tool_use.id`.

**Completion** — in the parent file, a `user` tool_result (matched by `tool_use_id`) whose
top-level `toolUseResult` is:

```json
{"status":"completed", "prompt":"…", "agentId":"a68d210d82716d08d", "agentType":"Explore",
 "content":[{"type":"text","text":"…final report…"}],
 "totalDurationMs":135715, "totalTokens":44067, "totalToolUseCount":31,
 "usage":{…final-message usage…},
 "toolStats":{"readCount":8,"searchCount":0,"bashCount":23,"editFileCount":0,
              "linesAdded":0,"linesRemoved":0,"otherToolCount":0}}
```

There are **no** `subagent_start` / `subagent_stop` record types — start/stop must be derived
from the Agent `tool_use` / its `tool_result`. Background agents additionally surface as
`queue-operation` records containing a `<task-notification>` block with `task-id`,
`tool-use-id`, `output-file`, `status`, `summary`.

---

## 8. File changes (Edit / Write)

**Edit** — `tool_use.input`: `{"file_path", "old_string", "new_string", "replace_all": false}`.
Result (`toolUseResult` on the user line):

```json
{"filePath":"…","oldString":"…","newString":"…","originalFile":"<full pre-edit content>",
 "structuredPatch":[{"oldStart":N,"oldLines":N,"newStart":N,"newLines":N,"lines":["-…","+…"]}],
 "userModified":false,"replaceAll":false}
```

**Write** — `tool_use.input`: `{"file_path", "content"}`. Result:

```json
{"type":"create","filePath":"…","content":"…","structuredPatch":[…],"originalFile":null|"…",
 "userModified":false}
```

`toolUseResult.type` distinguishes create vs overwrite for Write. `structuredPatch[].lines`
(prefixes `-`/`+`/space) gives additions/deletions counts and a reconstructable unified diff.
**MultiEdit does not exist in this corpus.** Deletion/rename of files happens only through
`Bash` (`rm`, `git mv`) — there is no dedicated tool record for it.

Other tool results with fixed shapes: `Bash` →
`{"stdout","stderr","interrupted","isImage","noOutputExpected"}`; `Read` →
`{"type":"text","file":{"filePath","content","numLines","startLine","totalLines"}}`.

---

## 9. Skills

Three independent signals, in order of reliability:

1. **`Skill` tool_use** (explicit invocation):
   `{"name":"Skill","input":{"skill":"gitnexus-exploring","args":"…"}}`. The skill body is then
   injected as a **`user` record with `isMeta: true`** and `sourceToolUseID` = that tool_use id
   (content starts with `"Base directory for this skill: …"`).
2. **`attributionSkill` / `attributionPlugin`** on subsequent assistant lines
   (e.g. `"superpowers:executing-plans"`) — attributes output to the active skill.
3. **Attachments**: `skill_listing` (available skills at session start), `invoked_skills`
   (`{"skills":[{"name","path","content"}]}` — skills auto-invoked with the prompt),
   `dynamic_skill` (`{"skillDir","skillNames":[…]}` — project-local skill dir discovery).

Slash commands invoked by the user appear inside the user prompt string as
`<command-name>…</command-name>` XML-ish tags (observed in tool-heavy sessions).

---

## 10. Record linking and ordering

- `uuid` — identity of each conversation record; `parentUuid` — its parent. The normal case
  is a linear chain (each record's `parentUuid` = previous conversation record's `uuid`),
  **but it is a tree, not a list**: with parallel tool calls, both `tool_use` lines chain
  linearly, while their two `tool_result` user lines each attach to *their own* tool_use
  line's uuid — i.e. siblings. Measured on the short session: 39/40 links linear, 1 fork.
- File order ≈ chronological (`timestamp` per line); checkpoint records (`last-prompt`,
  `mode`, …) are interleaved and have no uuid.
- `promptId` (user + sidechain records) groups all records belonging to one submitted prompt.
- `requestId`/`message.id` group the split assistant lines of one API call (§5).
- Compaction: `system/compact_boundary` has `parentUuid: null` +
  `logicalParentUuid: <pre-compact leaf uuid>` and
  `compactMetadata: {"trigger":"manual|auto","preTokens":…,"durationMs":…,"preservedSegment":{…}}`;
  the next chain starts from the `isCompactSummary` user record.
- `last-prompt.leafUuid` points at the current chain leaf uuid.

### `system` subtypes (corpus counts)

`turn_duration` 1076 (`{"durationMs":335597,"messageCount":40}` — end-of-turn marker, the
closest thing to a "turn boundary" record), `api_error` 387 (`{"error":{"message","status",
"requestId","formatted",…},"retryInMs","retryAttempt","maxRetries"}`), `local_command` 17,
`compact_boundary` 16, `scheduled_task_fire` 13, `stop_hook_summary` 4,
`model_refusal_fallback` 1, `informational` 1. System records carry `level`
(`info`/`error`).

### `attachment.attachment.type` (28 observed)

`output_style` 9310, `hook_success` 7670, `hook_system_message` 1564, `task_reminder` 1556,
`hook_non_blocking_error` 536, `queued_command` 314, `edited_text_file` 231,
`deferred_tools_delta` 175, `opened_file_in_ide` 167, `hook_additional_context` 155,
`skill_listing` 140, `mcp_instructions_delta` 137, `agent_listing_delta` 134,
`command_permissions` 65, `file` 60, `nested_memory` 56, `date_change` 40, `agent_mention` 34,
`dynamic_skill` 27, `auto_mode` 28, `selected_lines_in_ide` 22, `compact_file_reference` 16,
`ultrathink_effort` 7, `diagnostics` 3, `invoked_skills` 2, `plan_mode` 2, `plan_mode_exit` 2,
`plan_mode_reentry` 1. Hook attachments carry `hookName` (`"SessionStart:startup"`),
`hookEvent`, `stdout`, `toolUseID`.

---

## 11. Mapping: JSONL line → swarmery-design.md tables

| JSONL source | Target table | Notes / what goes into `payload` |
|---|---|---|
| file name + dir slug | `sessions.session_uuid`, `projects.slug` | confirmed: file name = session UUID |
| any envelope record | `sessions.cwd`, `sessions.git_branch` | take from first record; branch may change mid-file (keep first or last — decide at Gate 03) |
| first `assistant.message.model` | `sessions.model` | model is per-message; store main-chain dominant model |
| `ai-title.aiTitle` | `sessions.title` | better than "first prompt truncated"; fall back to first `user` string content |
| first/last `timestamp` | `sessions.started_at/ended_at` | **no session_end record exists**; `ended_at` = last line ts (or NULL while file is hot) |
| `user` with string content, `promptSource: typed\|queued\|sdk` | `turns` (role=user) + `events.type=user_prompt` | skip `isMeta`, `isCompactSummary` records |
| all `assistant` lines sharing one `message.id` | one `turns` row (role=assistant) | started_at = first split-line ts, ended_at = last; tokens from `usage` **once** |
| `assistant` `tool_use` block | `events.type=tool_call`, `tool_name=name` | payload = `input` (truncate `prompt`/`content` fields); `status`/`duration` filled when result arrives |
| matching `user` `tool_result` line | closes the `tool_call` event | `status` = `is_error ? 'error' : 'ok'`; payload += `toolUseResult` (minus `originalFile`) |
| `tool_use name=Agent` | `events.type=subagent_start` | payload = `{description, subagent_type}`; `agentId` known only at completion or via `meta.json` |
| Agent completion `toolUseResult` | `events.type=subagent_stop` | `duration_ms=totalDurationMs`; payload = `{agentId, agentType, status, totalTokens, toolStats}` |
| `subagents/agent-<id>.jsonl` lines | same `session_id`, events attributed via `agents` FK | join to parent event through `meta.json.toolUseId` |
| `tool_use name=Skill` + `attributionSkill` | `events.type=skill_use`, `skills` FK | payload = `{skill, args}` |
| Edit/Write `toolUseResult.structuredPatch` | `file_changes` | `change_type`: Edit→`edit`, Write→`toolUseResult.type` (`create`) or `edit`; additions/deletions = count of `+`/`-` lines; `diff` reconstructed from structuredPatch |
| `system subtype=api_error` | `events.type=error` | payload = `error` object |
| `system subtype=turn_duration` | `turns.ended_at` refinement | `durationMs`, `messageCount` |
| `system subtype=compact_boundary` | `events` (payload = `compactMetadata`) | also candidate for session lineage |
| `pr-link` | `events.type=commit`-adjacent (payload) | schema has no PR event type — Gate 03 |
| `queue-operation`, checkpoint records, `attachment` | ignore or `events` payload | attachments are context, not actions; recommend ignore in MVP |

### Deriving `turns.seq`

The format has **no sequence number**. Recommendation: `seq` = 0-based counter incremented
in **file order** over “turn openers”:
- a `user` record with string `content` and `isMeta`≠true (role=user turn);
- the **first** `assistant` line of each new `message.id` (role=assistant turn).

File order is stable (append-only log), so re-parsing yields identical `seq` →
`UNIQUE(session_id, seq)` holds. Tool-result user lines do NOT open turns — they belong to
the enclosing assistant turn. Note this means several consecutive assistant turns per user
turn (one per API call) — see contradiction C2.

### Contradictions with the design schema (for Gate 03)

- **C1 — token usage is per API message, duplicated per split line.** `turns.tokens_*`
  works only if the parser groups by `message.id` and counts usage once. Naive per-line
  summation inflates tokens ~2-4x. Also user turns never have token data (columns stay NULL).
- **C2 — "turn" granularity.** Design implies user/assistant alternation; reality is 1 user
  turn → N assistant API messages (each with its own usage) interleaved with tool_results.
  Either allow consecutive assistant turns (recommended, keeps per-message tokens exact) or
  aggregate an entire assistant volley into one turn (loses per-message model/usage detail).
- **C3 — `dedup_key = session_uuid:line_number` breaks for sidechains and split lines.**
  Sidechain files share the parent `sessionId` but restart line numbering → collisions.
  Every conversation record has a globally unique `uuid` — use it (checkpoint records have
  no uuid; for them hash the raw line). Also: files are rewritten with checkpoint records
  interleaved at arbitrary points, so line numbers are less stable than uuids.
- **C4 — `sessions.parent_uuid` (resume/fork) has no observed source field.** Continuation
  after compaction is signaled by `isCompactSummary`, but the record does **not** name the
  predecessor session UUID. Lineage needs a heuristic (or the field stays NULL in MVP).
- **C5 — no `session_end` event and no session `status` in the format.** `active` vs
  `completed` must be inferred (file mtime / tail `turn_duration`).
- **C6 — subagent tool is `Agent`, not `Task`** (and `subagent_start/stop` don't exist as
  records). Design §events comment lists `Task`; parser must match `Agent` (and possibly
  `Task` for pre-2.1 backfill — unverified).
- **C7 — `events.status='denied'` / `permission_request` rows have no JSONL source.**
  Only `permission-mode` checkpoints and `command_permissions` attachments exist. Denials
  presumably need the hooks (live) source, not JSONL backfill.
- **C8 — file deletes/renames invisible** to `file_changes` (only Bash commands do them);
  `change_type=delete|rename` will never be produced by the JSONL parser.

---

## Open questions

1. **Pre-2.1 format**: every file on this machine is ≥2.1.111. Community docs mention
   `summary` records, inline `isSidechain:true` sidechains, and a `Task` tool — none observed.
   Does backfill need to support the old inline-sidechain layout?
2. **Checkpoint record semantics**: why are `last-prompt`/`mode`/`permission-mode`/`ai-title`
   re-emitted (up to 149×/file)? Appears to be per-prompt state snapshots; exact trigger
   unconfirmed. Parser treats latest occurrence as current value.
3. **`file-history-snapshot`** (`messageId`, `trackedFileBackups`, `isSnapshotUpdate`) —
   presumably the /rewind checkpoint feature; relationship to `file_changes` unverified.
4. **`bridge-session`** (`bridgeSessionId: "cse_…"`) and **`agent-name`** records — appear
   related to claude.ai/remote bridging and named background agents; semantics unconfirmed.
5. **`queue-operation`** `remove` vs `dequeue` distinction unconfirmed (enqueue 572,
   remove 334, dequeue 226); `content` present only on some enqueues.
6. **Is `version` constant within one file?** Not verified across a resumed session.
7. **`usage.iterations[]`** — always length 1 in samples; when does it have >1 entry?
8. **`stop_details`, `diagnostics`** on assistant messages — shapes not catalogued.
9. **Skill completion**: there is no explicit "skill ended" marker; `attributionSkill`
   simply stops appearing. Duration of `skill_use` events is therefore approximate.
10. **`toolUseResult` for MCP tools** — shape not examined (low counts in corpus).
11. **Concurrent writes**: are main-file lines ever rewritten in place (vs append-only)?
    ~~Checkpoint records suggest occasional rewrites; needs a watch-based experiment before
    the ingest daemon assumes tail-follow is safe.~~
    **RESOLVED (2026-07-12, step 06 watch experiment): transcripts are append-only.**
    Method: the 4 most recently modified transcripts (1 main + 3 sidechain files, all
    being actively written by live Claude Code sessions) were polled every 5 s for 70 s,
    recording size, inode, and the SHA-256 of the first *initial-size* bytes. Result
    across all 14 polls × 4 files: sizes only ever grew (e.g. 198 815 → 242 097 bytes,
    880 097 → 906 851 bytes), the byte-prefix hash **never changed**, and inodes were
    stable — no truncation, no in-place rewrite, no rename-replace. Checkpoint records
    (`last-prompt`, `mode`, …) are *appended* state snapshots, not edits of earlier lines.
    Consequence for ingest: tail-follow from a persisted byte offset is safe. The daemon
    still keeps two defensive resets (offset → 0 when the stat inode differs from the
    stored one, or when the file shrank below the stored offset) to survive file
    recreation — e.g. a deleted and re-run session — with dedup absorbing the re-read.
