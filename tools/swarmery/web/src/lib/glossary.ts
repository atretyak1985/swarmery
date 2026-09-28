// The single source of explainer copy for the dashboard. <Explain id="…"/> and
// <HowItWorks id="…"/> both read from here; docs/concepts.md carries the long
// form and is kept in lockstep by internal/docsfs/glossary_drift_test.go.
//
// Copy budget, enforced by review: `short` ≤ 220 chars, `actions` ≤ 4 entries.
// Anything longer belongs in the doc behind "Read more →".
//
// `tone` lives here rather than at the call site on purpose — a concept is
// either reference ('explain', renders "?") or a demand for operator action
// ('action', renders "!"), and it must read the same everywhere it appears.

export type Tone = 'explain' | 'action';

export interface Concept {
  /** Display name, also the heading text in docs/concepts.md. */
  term: string;
  /** 1–2 sentences: what this is. Always visible when the popover opens. */
  short: string;
  tone: Tone;
  /** What the operator should do about it. */
  actions?: string[];
  /** Numbered walkthrough — the "how it works" block. */
  steps?: { title: string; body: string }[];
  /** Hard numbers, paths and env vars worth having in reach. */
  facts?: { label: string; value: string }[];
  /** Deep link into the docs pane: /docs/{slug}#{anchor}. */
  doc?: { slug: string; anchor: string };
}

// `satisfies` keeps the literal shape of every entry (that is what makes
// ConceptId and StepConceptId derivable) — but consumers must NOT read the
// literal type, because on a union of entry types an optional field that some
// members do not declare is unreadable. So the widening happens once, here.
const RAW = {
  handoff: {
    term: 'Handoff',
    short:
      'The daemon pre-wrote a continuation brief because this session crossed 150k tokens of context. It is a parachute, not a problem — nothing is wrong with the session.',
    tone: 'explain',
    actions: [
      'Keep going if the session is still sharp — the chip is informational.',
      'Commit or stash your work first; the brief describes state, it does not save it.',
      'Open the rail’s Handoff section, read the brief, then "copy resume command".',
      'Stop the session and run that command in a fresh terminal.',
    ],
    facts: [
      { label: 'threshold', value: '150k ctx' },
      { label: 'regenerates after', value: '+75k ctx' },
      { label: 'written to', value: '~/.swarmery/handoffs/<uuid>.md' },
      { label: 'disable', value: 'SWARMERY_HANDOFF=off' },
    ],
    doc: { slug: 'concepts', anchor: 'handoff' },
  },

  'fat-session': {
    term: 'Context footprint',
    short:
      'How full the model’s window is on the newest assistant turn (input + cache read + cache write). At a near-full window every further turn re-reads almost everything — this is the main cost driver.',
    tone: 'explain',
    actions: [
      'Amber (150k): consider /compact or splitting the work.',
      'Red (300k): every turn is now expensive — restart on a clean context.',
      'Plan several short sessions rather than one marathon.',
    ],
    facts: [
      { label: 'chip appears', value: '≥150k (amber)' },
      { label: 'danger', value: '≥300k (red)' },
      { label: 'handoff trigger', value: '150k' },
    ],
    doc: { slug: 'concepts', anchor: 'context-footprint' },
  },

  'proc-badge': {
    term: 'Orphaned / dead',
    short:
      '"orphaned" means the transcript is still live but the daemon can no longer see a matching process; "dead" means the process is gone. Either way nothing is advancing this session.',
    tone: 'action',
    actions: [
      'Reattach in the terminal where it was started, if that terminal is still open.',
      'Otherwise close it out with whichever control the row offers — Stop while it still reads as active, Kill once it reads as stuck.',
      'If the work matters, resume from the session’s Handoff brief instead.',
    ],
    doc: { slug: 'concepts', anchor: 'orphaned-dead' },
  },

  'kill-vs-stop': {
    term: 'Stop vs Kill',
    short:
      'Both send SIGTERM and escalate to SIGKILL after a grace period. What differs is the status recorded: Stop writes completed, which ingest may revert if the session speaks again; Kill writes killed, which is terminal.',
    tone: 'explain',
    actions: [
      'Prefer Stop — it needs no PID, so it also closes out a zombie row Kill refuses.',
      'Kill needs a live, identity-checked PID in a running or orphaned state.',
      'Only "Force kill", offered 10s after a Kill, sends SIGKILL straight away.',
    ],
    facts: [
      { label: 'both signal', value: 'SIGTERM → SIGKILL' },
      { label: 'grace', value: '5s · SWARMERY_KILL_ESCALATION' },
      { label: 'stop records', value: 'completed (revertible)' },
      { label: 'kill records', value: 'killed (terminal)' },
    ],
    doc: { slug: 'concepts', anchor: 'stop-vs-kill' },
  },

  'attach-detach': {
    term: 'Attach / Detach',
    short:
      'Attach wires a project into swarmery — merged settings entries, project.json, statusline and hooks. Detach is its exact mirror. Both preview the change before writing anything.',
    tone: 'explain',
    actions: [
      'Read the dry-run preview before confirming — it lists every file that would change.',
      'Lines starting with "!" flag foreign values the merge refused to overwrite; resolve those by hand.',
      'Each writes a .bak before rewriting settings.json; a full detach also backs up project.json.',
      'Attach restores project.json from that backup only when the file itself is gone.',
    ],
    doc: { slug: 'concepts', anchor: 'attach-detach' },
  },

  'session-outcome': {
    term: 'Session outcome',
    short:
      'Your verdict on a finished session — success, fail, or abandoned. It is set by hand and never inferred, which is what makes it trustworthy in the analytics.',
    tone: 'explain',
    actions: [
      'Label sessions as you close them; unlabelled sessions are invisible to outcome analytics.',
      'Use "abandoned" for work you walked away from — it is not the same signal as "fail".',
    ],
    doc: { slug: 'concepts', anchor: 'session-outcome' },
  },

  'playbook-stages': {
    term: 'Playbooks',
    short:
      'A playbook is a selectable execution recipe: an ordered chain of stages, each run as its own headless pass, all sharing the task’s single worktree.',
    tone: 'explain',
    steps: [
      {
        title: 'Pick a recipe on the board',
        body: 'Every board task can select a playbook in its drawer (or at quick-entry). No selection means the standard recipe — a single implementation pass, identical to pre-playbook dispatch.',
      },
      {
        title: 'Stages run sequentially',
        body: 'The dispatcher runs each stage as its own headless pass in the task’s one worktree. {task_prompt} injects the task text, and {previous_stage_output} hands one stage’s full reply to the next — that’s how plan-first feeds its plan into implement, and review-heavy critiques the diff it just produced.',
      },
      {
        title: 'The verify knob sets the bar',
        body: 'Each playbook declares strict, normal, or off. The verifier’s read-only contract and its PASS/FAIL/INCONCLUSIVE vocabulary are identical at every level — strict only adds one clause demanding each criterion be positively demonstrated (review-heavy uses it), and off skips the run before a prompt is built, so no verdict is stamped.',
      },
      {
        title: 'Make it your own',
        body: 'Built-ins ship inside the daemon and are read-only. “Duplicate to project” copies the markdown into <project>/.claude/playbooks/ where its prompts become editable; a project file with the same name overrides the built-in. Frontmatter takes name, description, verify, and optional model and permission_mode overrides.',
      },
    ],
    // `steps` is not rendered in the popover (see Explain.tsx) — these facts are
    // what the trigger shows, so they carry the reference load on their own.
    facts: [
      { label: 'built-ins', value: 'standard · plan-first · review-heavy' },
      { label: 'no selection', value: 'auto-profiled at dispatch, then stamped on the card' },
      { label: 'make your own', value: '.claude/playbooks/<name>.md' },
    ],
    doc: { slug: 'concepts', anchor: 'playbooks' },
  },

  'verify-knob': {
    term: 'Verify level',
    short:
      'How hard the trajectory verifier judges a stage. Its read-only contract and PASS/FAIL/INCONCLUSIVE vocabulary are fixed at every level; the knob moves exactly one line of the prompt — the bar.',
    tone: 'explain',
    actions: [
      'Use strict for review stages, where a false pass is the expensive error.',
      'Strict adds one clause: every criterion positively demonstrated, not merely plausible.',
      'Use off only for stages with no reviewable output — it means no verdict, not a passing one.',
    ],
    doc: { slug: 'concepts', anchor: 'verify-level' },
  },

  worktree: {
    term: 'Task worktree',
    short:
      'Each board task gets one git worktree on its own swarm/<task-id> branch. Every stage of the playbook runs in that same worktree, so later stages see earlier stages’ edits.',
    tone: 'explain',
    actions: [
      'If dispatch reports a busy branch, the task branch is checked out elsewhere — resolve it by hand; the daemon will not silently rename.',
      'Never point a task at the repo root; the daemon refuses that outright.',
    ],
    doc: { slug: 'concepts', anchor: 'task-worktree' },
  },

  'permission-mode': {
    term: 'Headless permission mode',
    short:
      'A headless run has no one to answer a permission prompt, so an unanswered ask is auto-denied — and the run still exits 0. Every spawn site therefore passes --permission-mode, defaulting to bypassPermissions.',
    tone: 'explain',
    actions: [
      'Leave the default unless you must sandbox tighter — acceptEdits cannot commit, so a phase can never finish under it.',
      'bypassPermissions skips the ask, not the deny list — permissions.deny rules still apply.',
      'Pin a different mode per spawn site with the SWARMERY_*_PERMISSION_MODE env vars; "off" omits the flag entirely.',
    ],
    facts: [
      { label: 'default', value: 'bypassPermissions · all spawn sites' },
      { label: 'per-site', value: 'SWARMERY_{DISPATCH,PLANRUN,PHASERUN}_PERMISSION_MODE' },
      { label: 'all sites', value: 'SWARMERY_PERMISSION_MODE · off omits the flag' },
      { label: 'still enforced', value: 'permissions.deny · swarm/ worktree isolation' },
    ],
    doc: { slug: 'concepts', anchor: 'headless-permission-mode' },
  },

  'planning-mode': {
    term: 'Planning Mode',
    short:
      'A headless planner interviews you one question at a time and writes a phased plan into the private workspace.',
    tone: 'explain',
    steps: [
      {
        title: 'Describe the idea',
        body: 'A headless planner session starts in this project’s repo — it sees the code, CLAUDE.md, and the core-pack planning agents.',
      },
      {
        title: 'Answer structured questions',
        body: 'The planner interviews you one question at a time — pick an option (or write your own) while the running plan rebuilds beside it after every answer.',
      },
      {
        title: 'Refine or proceed',
        body: '"Refine" steers the plan and the next questions; "Continue with the plan" ends the interview and the planner writes the full plan.',
      },
      {
        title: 'The plan lands in the workspace',
        body: 'Phase-N docs with acceptance checkboxes are written to the private workspace — never the repo — and appear on the Plans page within seconds.',
      },
    ],
    // As with playbook-stages: `steps` stays out of the popover, so these facts
    // answer the one thing the page subtitle does not — where "the private
    // workspace" actually is.
    facts: [
      { label: 'plans land in', value: '<root>/<project>/workspace/working/' },
      { label: 'root', value: 'AGENT_WORKSPACE_ROOT · default ~/swarmery-workspace' },
      { label: 'plan docs', value: 'yours to edit; swarmery ticks acceptance boxes as phases finish' },
    ],
    doc: { slug: 'concepts', anchor: 'planning-mode' },
  },

  'claude-account': {
    term: 'Claude account',
    short:
      'A Claude Code identity on this machine: one config directory plus one credential. The directory names the account — ~/.claude-work is the account "work"; ~/.claude is the default.',
    tone: 'explain',
    actions: [
      '"+ add account" reserves the key and shows a login command — run it in your own terminal; swarmery never logs in for you.',
      'Use a private browser window for /login if it is a different subscription.',
      'A grey "unknown" dot means the daemon runs with SWARMERY_USAGE_OAUTH=0, not that the account is broken.',
    ],
    facts: [
      { label: 'config dir', value: '~/.claude-<key>' },
      { label: 'default', value: '~/.claude · not removable' },
      { label: 'credential', value: 'written by the Claude CLI, per-dir Keychain item' },
    ],
    doc: { slug: 'concepts', anchor: 'claude-account' },
  },

  'account-binding': {
    term: 'Account binding',
    short:
      'The project-level choice of which Claude account its sessions run under. Resolved once, at process spawn — a run already in flight keeps its account until it finishes.',
    tone: 'explain',
    actions: [
      'Bind here or with /account use <key> in a session; both write the same file.',
      'Changing it never touches a running process — it applies from the next spawn.',
      'Clear the binding to fall back to the machine default account.',
    ],
    facts: [
      { label: 'stored in', value: '.claude/settings.local.json · gitignored' },
      { label: 'unbound project', value: 'no CLAUDE_CONFIG_DIR set — behaves exactly as before' },
      { label: 'covers', value: 'dispatch · verify · planning · terminal dock · shell function' },
    ],
    doc: { slug: 'concepts', anchor: 'account-binding' },
  },

  // ── /retro ────────────────────────────────────────────────────────────────
  // The page's own complaint was that it explains nothing: it renders numbers
  // and two buttons whose real blast radius is invisible. Every block below
  // therefore answers the same three things — what is measured, from which
  // source, and what to do with it — and the two action concepts say plainly
  // what their button does and does NOT do.

  'retro-page': {
    term: 'Retro page',
    short:
      'How the agent system is performing over a window, and the one loop that changes it: measure → analyze → recommend → review → plan.',
    tone: 'explain',
    steps: [
      {
        title: 'Read the window',
        body: 'Every block on this page is one window (14 days by default) folded out of session transcripts already in SQLite — runs, cost, errors, denials, waits, and the workspace artifacts your tasks wrote.',
      },
      {
        title: 'Analyze — deterministic, no model',
        body: '"Analyze now" runs a local rule engine (R1–R9) plus the trajectory evaluator over that data. No LLM is called, nothing is spent, and the same data always yields the same recommendations.',
      },
      {
        title: 'Decide on the recommendations',
        body: 'Accepting one snapshots the metric it fired on as a baseline. Adoption is then detected automatically for some targets, and verification compares the metric against that baseline a week later.',
      },
      {
        title: 'Improve one agent, or the whole system',
        body: 'A scorecard’s Improve button rewrites exactly one agent definition file as a reviewable diff. The page-level Improve reads the whole report and writes an analysis of the system — agents, skills, commands, hooks, processes.',
      },
      {
        title: 'Turn an accepted analysis into a plan',
        body: 'Nothing is written to a repository until you accept an analysis. Accepting hands it to Planning Mode as the idea for a normal planning interview, and the plan lands in the private workspace.',
      },
    ],
    facts: [
      { label: 'default window', value: '14 local days, ending today' },
      { label: 'source', value: 'sessions, turns, events, tasks — already-ingested SQLite' },
      { label: 'one call', value: 'GET /api/retro/report — all sections + a citable digest' },
    ],
    doc: { slug: 'concepts', anchor: 'retro-page' },
  },

  'retro-kpis': {
    term: 'Retro KPIs',
    short:
      'Window totals with an arrow comparing them to the previous window of the same length: agent spend, runs, and the runs that hit an error. The orchestrator is counted separately from subagents.',
    tone: 'explain',
    actions: [
      'Read the arrow, not the number — the absolute total moves with how much you worked.',
      'A rising error share with flat runs is the signal worth acting on.',
    ],
    facts: [
      { label: 'compared against', value: 'the previous window of equal length' },
      {
        label: 'orchestrator',
        value: 'has no subagent_start of its own, so it reports no run count',
      },
      { label: 'approximate', value: 'shown when the window overlaps rolled-up (pruned) days' },
    ],
    doc: { slug: 'concepts', anchor: 'retro-kpis' },
  },

  'retro-scorecard': {
    term: 'Agent scorecard',
    short:
      'One card per subagent: runs, sessions, cost, p95 duration, and an error rate that is the share of RUNS with at least one behavior-fixable error — not raw error events per run.',
    tone: 'explain',
    actions: [
      'Compare the error rate to the previous window before reacting to a single bad day.',
      'Harness and infrastructure noise is excluded, so what is left is arguably fixable in the prompt.',
      'Use Improve on a card only after the same weakness shows up twice.',
    ],
    facts: [
      { label: 'error rate', value: 'distinct runs with ≥1 behavior-fixable error ÷ runs' },
      { label: 'runs', value: 'subagent_start events, folded across naming notations' },
      { label: 're-dispatch', value: 'from the task_delegations ledger your retro docs wrote' },
    ],
    doc: { slug: 'concepts', anchor: 'agent-scorecard' },
  },

  'retro-recommendations': {
    term: 'Advisor recommendations',
    short:
      'What a deterministic rule engine (R1–R9) concluded from this data. Each carries the numbers it fired on and the sessions that produced them — evidence, not advice.',
    tone: 'action',
    actions: [
      'Accept one to snapshot its metric as a baseline; dismiss suppresses re-proposal for 30 days.',
      'Adoption is auto-detected only for agent, tool and process targets.',
      'An error-group or config recommendation verifies straight from accepted — it never shows "adopted".',
    ],
    facts: [
      { label: 'lifecycle', value: 'proposed → accepted | dismissed → adopted → verified' },
      { label: 'verification', value: 'metric ≥20% better than baseline, ≥7 days after adoption' },
      { label: 'identity', value: 'one row per rule:target, aggregated across projects' },
    ],
    doc: { slug: 'concepts', anchor: 'advisor-recommendations' },
  },

  'retro-analyze-button': {
    term: 'Analyze now',
    short:
      'Re-runs the rule engine and the local trajectory evaluator over the data already in the database. It calls NO model: it is free, repeatable, and deterministic on the same input.',
    tone: 'explain',
    actions: [
      'Press it after new sessions land — recommendations only change when the data does.',
      'It cannot cost you anything, so there is no reason to ration it.',
      'For the model-written analysis of the whole report, use the page-level Improve instead.',
    ],
    facts: [
      { label: 'runs', value: 'advisor rules R1–R9 + trajeval — both local' },
      {
        label: 'LLM judge',
        value: 'deliberately NOT fired here; it runs on the daemon’s 24h schedule',
      },
      { label: 'scope', value: 'always fleet-wide — cross-project rates would be wrong if narrowed' },
    ],
    doc: { slug: 'concepts', anchor: 'analyze-now' },
  },

  'retro-improve': {
    term: 'Improve the system',
    short:
      'Reads the whole window, has an agent write what hurts and what to change, every claim citing its evidence. It writes nothing anywhere until you accept it.',
    tone: 'action',
    actions: [
      'Read the citations, not the prose — a claim you cannot trace is one the contract should have rejected.',
      'Accept only what you would defend; dismissing costs nothing but a re-run.',
      'Accepting hands the change section to Planning Mode as the seed of a normal interview.',
    ],
    facts: [
      { label: 'unlike Analyze now', value: 'this one does call a model, and it costs tokens' },
      { label: 'unlike per-agent Improve', value: 'covers agents, skills, commands, hooks, processes' },
      { label: 'rejected when', value: 'the analysis cites nothing, or cites an id the report never had' },
      { label: 'one at a time', value: 'a second start returns "already running", not a second run' },
    ],
    doc: { slug: 'concepts', anchor: 'improve-the-system' },
  },

  'retro-agent-improve': {
    term: 'Agent improve',
    short:
      'Generates a minimal unified diff to EXACTLY ONE agent definition file, from that agent’s own evidence. It changes nothing else — not skills, not commands, not hooks.',
    tone: 'action',
    actions: [
      'Preview the evidence bundle first; the button opens on it, not on the diff.',
      'Only one open proposal exists per agent at a time — decide the current one first.',
      'Review the diff yourself: approving it is what applies the change.',
    ],
    facts: [
      { label: 'target', value: 'one plugins/<pack>/agents/<name>.md, resolved at origin/main' },
      { label: 'budget', value: 'the prompt demands a minimal change, ≤120 changed lines' },
      { label: 'lifecycle', value: 'proposed → approved → applied | rejected; failed is retriable' },
    ],
    doc: { slug: 'concepts', anchor: 'agent-improve' },
  },

  'retro-proposals': {
    term: 'Agent proposals',
    short:
      'The diffs Improve has generated but you have not decided on yet. Each is pinned to the file content it was written against, so a stale diff fails to apply rather than clobbering newer edits.',
    tone: 'action',
    actions: [
      'Read the diff and the per-hunk rationale before approving.',
      'Approving applies it against the marketplace clone, not your working tree.',
      'A failed proposal keeps its error and can be retried without regenerating from scratch.',
    ],
    facts: [
      { label: 'pinned to', value: 'sha256 of the agent file at generation time' },
      { label: 'invariant', value: 'one open proposal per agent' },
    ],
    doc: { slug: 'concepts', anchor: 'agent-proposals' },
  },

  'retro-judgments': {
    term: 'Trajectory judgments',
    short:
      'An LLM judge’s 1–5 scores for completed sessions across a few dimensions. Advisory only — it informs the scorecards’ success rate, it does not gate anything.',
    tone: 'explain',
    facts: [
      { label: 'runs', value: 'on the daemon’s 24h schedule, never on "Analyze now"' },
      { label: 'coverage', value: 'only sessions the judge has scored appear here' },
    ],
    doc: { slug: 'concepts', anchor: 'trajectory-judgments' },
  },

  'retro-friction': {
    term: 'Friction board',
    short:
      'Where the system stalls rather than fails: tools that got denied, the error signatures that fire most, and how long approvals kept an agent waiting.',
    tone: 'action',
    actions: [
      'A repeatedly denied tool with no rule is the cheapest fix on this page — add the rule inline.',
      'Pending approvals are counted as of NOW, not within the window: an old one still blocks work today.',
    ],
    facts: [
      {
        label: 'denied tools',
        value: 'tool_call / skill_use / subagent_start events with status=denied',
      },
      { label: 'error groups', value: 'the same folding /api/stats/errors uses' },
      { label: 'waits', value: 'permission_requests, requested_at → resolved_at' },
    ],
    doc: { slug: 'concepts', anchor: 'friction-board' },
  },

  'retro-lessons': {
    term: 'Lessons learned',
    short:
      'Lessons your own retrospective docs recorded, parsed out of the private workspace and joined to the tasks that produced them. Written by agents and by you — not inferred from telemetry.',
    tone: 'explain',
    actions: [
      'Search it before starting similar work — this is the memory the numbers cannot hold.',
      'An empty feed means the tasks in range wrote no retrospective, not that nothing was learned.',
    ],
    facts: [
      { label: 'source', value: '09-retrospective.md docs in the private workspace' },
      { label: 'filtered on', value: 'the task’s start date, newest first, capped at 100' },
    ],
    doc: { slug: 'concepts', anchor: 'lessons-learned' },
  },

  'retro-estimation': {
    term: 'Estimation accuracy',
    short:
      'Estimated versus actual hours per workspace task, with the loop count and the delegation ledger beside it. Only tasks that wrote at least one artifact appear.',
    tone: 'explain',
    actions: [
      'A large variance next to many loops means the task was underspecified, not underestimated.',
      'Read re-dispatch verdicts as routing feedback: the wrong agent was picked, not a bad agent.',
    ],
    facts: [
      { label: 'source', value: 'task retro docs, loop journals and the delegation ledger' },
      { label: 'cap', value: '200 tasks, newest first' },
    ],
    doc: { slug: 'concepts', anchor: 'estimation-accuracy' },
  },

} satisfies Record<string, Concept>;

export type ConceptId = keyof typeof RAW;

/** The registry as consumers see it: every optional field readable on every id. */
export const CONCEPTS: Record<ConceptId, Concept> = RAW;

/** A concept that is guaranteed to carry a walkthrough. */
export type StepConcept = Concept & { steps: NonNullable<Concept['steps']> };

/** The ids whose entry actually declares `steps`. <HowItWorks id="handoff"/> is
 * therefore a compile error rather than a component that silently renders null. */
export type StepConceptId = {
  [K in ConceptId]: (typeof RAW)[K] extends { steps: unknown } ? K : never;
}[ConceptId];

/** The step-carrying subset, so <HowItWorks> reads a non-optional array. */
export const STEP_CONCEPTS: Record<StepConceptId, StepConcept> = RAW;

/** Code → UI vocabulary (Canvas v3 artboard 1). The code name stays as a small caption. */
export const UI_TERMS = {
  surprise: { code: 'surprise index', ui: 'off-plan', hint: 'how far the run landed from the plan' },
  prior: { code: 'prior', ui: "planner's guess", hint: '' },
  posterior: { code: 'posterior', ui: 'after reading the code', hint: '' },
  shadow: { code: 'shadow', ui: 'watching', hint: '' },
  active: { code: 'active', ui: 'acting', hint: '' },
  agreement: { code: 'agreement', ui: 'matches you', hint: '' },
  groundTruth: { code: 'ground truth', ui: 'what actually happened', hint: '' },
  recommendation: { code: 'recommendation', ui: 'the Advisor noticed…', hint: '' },
  retirement: { code: 'retirement', ui: 'stop using this lesson?', hint: '' },
  calibration: { code: 'calibration', ui: 'how honest forecasts are', hint: '' },
} as const;
