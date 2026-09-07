// Typed client for the swarmery REST API.
// All response types live in ./api/types.ts (frozen contract) — do not
// declare API types here.

import type {
  AccountBinding,
  AccountsResponse,
  AdviseStats,
  AgentEvidence,
  AnalyticsDimension,
  AnalyticsMetric,
  ApprovalRule,
  AttachResponse,
  BoardColumn,
  BoardTask,
  BreakdownResp,
  AddConnectorInput,
  Connector,
  ConnectorsResponse,
  DetachResponse,
  DiscardTaskResponse,
  DispatchStatus,
  LandTaskResponse,
  TaskDiff,
  AutonomyResp,
  DocDetail,
  DocMeta,
  DurationsResp,
  Epic,
  ErrorsResp,
  FileSessionsResponse,
  FunnelResp,
  HealthResponse,
  MatrixResp,
  DuplicatePlaybookResponse,
  MemoryFileContent,
  MemoryListResp,
  OnboardConfig,
  PlaybookRollup,
  ProductivityResp,
  UsageLoginComplete,
  UsageLoginStart,
  UsageResp,
  AccountProbeResponse,
  OnboardRequest,
  OnboardResponse,
  Playbook,
  PermissionEscalation,
  PhaseDiagnosis,
  PermissionPresetInput,
  PermissionPresetView,
  PermissionRequest,
  PermissionRequestStatus,
  PlanDoc,
  PlanRevision,
  PlanRunMode,
  PlanningStart,
  PlanningStatus,
  RevisionConflict,
  RevisionFile,
  ProjectDetail,
  RunConflictCode,
  ProjectMeta,
  ProjectMetaPatch,
  ProjectConfigInvalid,
  ProjectConfigProbeResponse,
  ProjectConfigWriteResponse,
  ProjectOverviewResp,
  ProjectPluginsResponse,
  PluginRepairResponse,
  ProjectPluginToggleResponse,
  ProjectsHealthResponse,
  ProjectsResponse,
  ProposalsResp,
  ProvisionResponse,
  Recommendation,
  RecommendationsResp,
  RecommendationStatus,
  RemoveAccountResponse,
  RetroAgentsResp,
  RetroFrictionResp,
  RetroLessonsResp,
  RetroAnalysis,
  RetroAnalysisResp,
  RetroPlanConflict,
  RetroPlanStarted,
  RetroReportResp,
  RetroTasksResp,
  Routine,
  RoutineInput,
  RoutineRun,
  SearchResponse,
  ContextHogsReport,
  PendingSession,
  SessionDetailResponse,
  SessionHandoffResponse,
  SessionOutcome,
  SessionsResponse,
  StatsOverview,
  StatsToday,
  TaskDetail,
  SkillsResp,
  TasksResponse,
  TimeseriesResp,
  ToolsResp,
  ToolsResponse,
} from './api/types';
import { mockApi } from './mock/data';

/** Offline mock mode — fixture data + fake WS (VITE_MOCK=1). */
export const MOCK: boolean = import.meta.env.VITE_MOCK === '1';

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) {
    throw new Error(`GET ${path}: ${res.status}`);
  }
  return (await res.json()) as T;
}

export interface SessionFilters {
  /** Project slug or id (server matches either). */
  project?: string;
  status?: string;
  /**
   * Workspace task id of a plan — narrows the list to the sessions that plan
   * run produced (its controller, every phase run, and the subagents resolved
   * from the run worktree). Server-side, so the Plans panel and the Sessions
   * page group by the same rule.
   */
  planTask?: number;
}

export function fetchProjects(includeArchived = false): Promise<ProjectsResponse> {
  if (MOCK) return mockApi.projects();
  return get(`/api/projects${includeArchived ? '?include=archived' : ''}`);
}

/** GET /api/projects/{id} — enriched project + local components + stats. */
export function fetchProject(id: number | string): Promise<ProjectDetail> {
  if (MOCK) return mockApi.project(id);
  return get(`/api/projects/${encodeURIComponent(id)}`);
}

/** GET /api/projects/{id}/overview — Canvas v2 editorial aggregate (rightNow / thisWeek / attention). */
export function fetchProjectOverview(id: number | string): Promise<ProjectOverviewResp> {
  return get(`/api/projects/${encodeURIComponent(id)}/overview`);
}

/** DELETE /api/projects/{id} — soft-archive (remove from the default list). */
export async function archiveProject(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}`, { method: 'DELETE' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `archive failed: ${String(res.status)}`);
  }
}

/** POST /api/projects/{id}/restore — un-archive. */
export async function restoreProject(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}/restore`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `restore failed: ${String(res.status)}`);
  }
}

/** POST /api/projects/{id}/architecture/rebuild — force-regenerate the
 * architecture map via the provision pipeline (single-flight; 202 returns the
 * in-flight job when one is already running). */
export async function rebuildArchitectureMap(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}/architecture/rebuild`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `rebuild failed: ${String(res.status)}`);
  }
}

/** GET /api/projects/health — per-project week-over-week health rows. */
export function fetchProjectsHealth(): Promise<ProjectsHealthResponse> {
  if (MOCK) return mockApi.projectsHealth();
  return get('/api/projects/health');
}

/** PATCH /api/projects/{id} — update pinned / tags (dashboard-only meta). */
export async function patchProject(id: number, patch: ProjectMetaPatch): Promise<ProjectMeta> {
  if (MOCK) return { pinned: patch.pinned ?? false, tags: patch.tags ?? [] };
  const res = await fetch(`/api/projects/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `update failed: ${String(res.status)}`);
  }
  return (await res.json()) as ProjectMeta;
}

/**
 * POST /api/projects/{id}/detach — remove the swarmery-owned entries from the
 * project's .claude/settings.json. dryRun=true returns the plan without writing
 * (rendered as a preview before the real call); full=true also removes the
 * other onboarding artifacts (project.json, statusline scripts). 403 when the
 * endpoint is disabled (no SWARMERY_ONBOARD_ROOTS) or the path is outside the
 * allow-list.
 */
export async function detachProject(
  id: number,
  dryRun: boolean,
  full: boolean,
): Promise<DetachResponse> {
  if (MOCK) {
    return {
      detached: true,
      dryRun,
      steps: ['- enabledPlugins.core@swarmery', '- extraKnownMarketplaces.swarmery'],
      ...(dryRun ? {} : { backup: '.claude/settings.json.bak' }),
    };
  }
  const params = new URLSearchParams();
  if (dryRun) params.set('dryRun', '1');
  if (full) params.set('full', '1');
  const qs = params.size > 0 ? `?${params.toString()}` : '';
  const res = await fetch(`/api/projects/${String(id)}/detach${qs}`, {
    method: 'POST',
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `detach failed: ${String(res.status)}`);
  }
  return (await res.json()) as DetachResponse;
}

/**
 * POST /api/projects/{id}/attach — re-enable swarmery for a detached project:
 * merge the swarmery entries back into settings.json, restore project.json
 * from its .bak, reinstall hooks. dryRun=true returns the plan without writing.
 * 403 when the endpoint is disabled (no SWARMERY_ONBOARD_ROOTS / workspace
 * root) or the path is outside the allow-list.
 */
export async function attachProject(id: number, dryRun: boolean): Promise<AttachResponse> {
  if (MOCK) {
    return {
      attached: true,
      dryRun,
      steps: ['+ enabledPlugins.core@swarmery', '+ .claude/project.json restored from project.json.bak'],
      ...(dryRun ? {} : { backup: '.claude/settings.json.bak' }),
    };
  }
  const qs = dryRun ? '?dryRun=1' : '';
  const res = await fetch(`/api/projects/${String(id)}/attach${qs}`, {
    method: 'POST',
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `attach failed: ${String(res.status)}`);
  }
  return (await res.json()) as AttachResponse;
}

/** GET /api/projects/{id}/plugins — marketplace catalog × enabledPlugins. */
export function fetchProjectPlugins(id: number | string): Promise<ProjectPluginsResponse> {
  if (MOCK) return mockApi.projectPlugins();
  return get(`/api/projects/${encodeURIComponent(id)}/plugins`);
}

/**
 * PUT /api/projects/{id}/plugins/{name} — flip a pack in the project's
 * .claude/settings.json (merge-only, .bak backup on the daemon side). Takes
 * effect in the NEXT Claude Code session. 403 when the daemon write fence is
 * closed; 409 when settings.json is missing/malformed — surfaced inline.
 */
export async function toggleProjectPlugin(
  id: number,
  name: string,
  enabled: boolean,
): Promise<ProjectPluginToggleResponse> {
  if (MOCK) return { name, enabled, changed: true, backup: '.claude/settings.json.bak' };
  const res = await fetch(`/api/projects/${String(id)}/plugins/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `toggle failed: ${String(res.status)}`);
  }
  return (await res.json()) as ProjectPluginToggleResponse;
}

/**
 * POST /api/projects/{id}/plugins/{name}/repair — runs `claude plugin
 * install|update <id> --scope project` on the daemon side. The action is chosen
 * by the daemon from the current drift status, so the client cannot ask for an
 * install where an update is what is needed. Takes effect in the NEXT Claude
 * Code session, which is why the response always sets restart.
 *
 * Projects whose .claude is a symlinked overlay cannot take a project-scope
 * write at all; the daemon installs at user scope instead and says so in
 * `scope`. Check `warning` on success — it is set when that fallback could not
 * revert the global enable it caused.
 */
export async function repairProjectPlugin(
  id: number,
  pluginId: string,
): Promise<PluginRepairResponse> {
  if (MOCK)
    return {
      id: pluginId,
      action: 'install',
      scope: 'project',
      output: 'mock',
      status: 'ok',
      restart: true,
    };
  const res = await fetch(
    `/api/projects/${String(id)}/plugins/${encodeURIComponent(pluginId)}/repair`,
    { method: 'POST' },
  );
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string; output?: string };
    throw new Error(data.error ?? data.output ?? `repair failed: ${String(res.status)}`);
  }
  return (await res.json()) as PluginRepairResponse;
}

/**
 * Thrown by putProjectConfig on a 422: the value failed the pack's declared
 * schema. Carries the server's structured `problems` (one human line per
 * dotted field path, e.g. "repro.test is required") so a caller with
 * field-level UI (PluginConfigModal) can place each one next to its input
 * instead of only showing a joined sentence. Mirrors EscalationRequiredError
 * below — same "typed error carries the structured payload" shape.
 */
export class ConfigValidationError extends Error {
  readonly problems: string[];
  constructor(payload: ProjectConfigInvalid) {
    super(payload.error);
    this.name = 'ConfigValidationError';
    this.problems = payload.problems;
  }
}

/**
 * PUT /api/projects/{id}/config/{key} — write ONE top-level key of the
 * project's .claude/project.json. Every other key keeps its value, its position
 * in the file, and the file's 2-space formatting; the daemon backs the previous
 * contents up to project.json.bak first.
 *
 * Only keys a catalogued pack declared in its requirements.json are writable
 * (404 otherwise). 403 when the daemon write fence is closed or the project sits
 * outside it; 409 when there is no project.json to merge into; 422 when the
 * value fails the pack's declared schema — thrown as {@link ConfigValidationError}
 * so the caller can place each problem on its field; every other non-2xx
 * throws a plain Error with the server's message.
 */
export async function putProjectConfig(
  id: number,
  key: string,
  value: unknown,
): Promise<ProjectConfigWriteResponse> {
  if (MOCK) return { key, written: true, backup: '.claude/project.json.bak', changed: true };
  const res = await fetch(`/api/projects/${String(id)}/config/${encodeURIComponent(key)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as Partial<ProjectConfigInvalid>;
    if (res.status === 422 && data.problems !== undefined) {
      throw new ConfigValidationError({
        error: data.error ?? 'invalid config',
        problems: data.problems,
      });
    }
    throw new Error(data.error ?? `config write failed: ${String(res.status)}`);
  }
  return (await res.json()) as ProjectConfigWriteResponse;
}

/**
 * POST /api/projects/{id}/config/{key}/probe — ask a live `claude` session for
 * REAL candidate values for the fields the pack nominated. Writes nothing: the
 * answer only fills datalists, and saving stays an explicit `save`.
 *
 * Runtime failures are NOT exceptions here. A timeout, a missing binary, prose
 * instead of JSON — all of them come back 200 with empty `suggestions` and a
 * `reason` to show in grey. Only the refusals throw: 403 (fence), 404 (the key
 * declares no probe), and 400 with `problems` when the probe's own inputs are
 * still empty — thrown as {@link ConfigValidationError} so the modal can place
 * each problem on its field, exactly as it does for a rejected save.
 *
 * `signal` is worth passing: the server's timeout hangs off the REQUEST context,
 * so aborting the fetch kills the agent process instead of orphaning it for the
 * next three minutes.
 */
export async function probeProjectConfig(
  id: number,
  key: string,
  value: unknown,
  signal?: AbortSignal,
): Promise<ProjectConfigProbeResponse> {
  if (MOCK) return { suggestions: {}, reason: 'probe is unavailable in mock mode' };
  const res = await fetch(
    `/api/projects/${String(id)}/config/${encodeURIComponent(key)}/probe`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value }),
      // `?? null` because RequestInit.signal is AbortSignal | null under
      // exactOptionalPropertyTypes — undefined is not one of its inhabitants.
      signal: signal ?? null,
    },
  );
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as Partial<ProjectConfigInvalid>;
    if (res.status === 400 && data.problems !== undefined) {
      throw new ConfigValidationError({
        error: data.error ?? 'the probe needs more fields filled in first',
        problems: data.problems,
      });
    }
    throw new Error(data.error ?? `probe failed: ${String(res.status)}`);
  }
  return (await res.json()) as ProjectConfigProbeResponse;
}

/** GET /api/projects/onboard/config — defaults + enabled state for the modal. */
export function fetchOnboardConfig(): Promise<OnboardConfig> {
  return get('/api/projects/onboard/config');
}

/**
 * POST /api/projects/onboard — bootstrap a new consumer project (.claude/
 * settings.json + project.json + workspace namespace). The endpoint is fenced
 * to an allow-list and returns 403 when disabled; non-2xx throws the server's
 * error text so the form can surface it inline. An empty workspaceRoot falls
 * back to the server default.
 */
export async function onboardProject(
  slug: string,
  path: string,
  packs: string[],
  workspaceRoot?: string,
): Promise<OnboardResponse> {
  const body: OnboardRequest = { slug, path, packs };
  if (workspaceRoot !== undefined && workspaceRoot !== '') body.workspaceRoot = workspaceRoot;
  const res = await fetch('/api/projects/onboard', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `onboard failed: ${String(res.status)}`);
  }
  return (await res.json()) as OnboardResponse;
}

export interface SessionPageOpts {
  /** Server default 100, max 500. */
  limit?: number;
  /** nextCursor of the previous page. */
  cursor?: string;
}

export function fetchSessions(
  filters: SessionFilters = {},
  page: SessionPageOpts = {},
): Promise<SessionsResponse> {
  if (MOCK) return mockApi.sessions(filters);
  const qs = new URLSearchParams();
  if (filters.project !== undefined) qs.set('project', filters.project);
  if (filters.status !== undefined) qs.set('status', filters.status);
  if (filters.planTask !== undefined) qs.set('planTask', String(filters.planTask));
  if (page.limit !== undefined) qs.set('limit', String(page.limit));
  if (page.cursor !== undefined) qs.set('cursor', page.cursor);
  const query = qs.toString();
  return get(`/api/sessions${query === '' ? '' : `?${query}`}`);
}

/** Narrows the 202 "not ingested yet" answer from the 200 session detail. */
export function isPendingSession(r: SessionDetailResponse): r is PendingSession {
  return 'pending' in r && r.pending === true;
}

export function fetchSession(id: number | string): Promise<SessionDetailResponse> {
  if (MOCK) return mockApi.session(id);
  return get(`/api/sessions/${encodeURIComponent(id)}`);
}

export function fetchSessionHandoff(id: number | string): Promise<SessionHandoffResponse> {
  if (MOCK) return mockApi.sessionHandoff(id);
  return get(`/api/sessions/${encodeURIComponent(id)}/handoff`);
}

export function fetchSessionContextHogs(id: number | string): Promise<ContextHogsReport> {
  if (MOCK) return mockApi.sessionContextHogs(id);
  return get(`/api/sessions/${encodeURIComponent(id)}/context-hogs`);
}

export function fetchStatsToday(): Promise<StatsToday> {
  if (MOCK) return mockApi.statsToday();
  return get('/api/stats/today');
}

/** Day-scoped overview stats + trailing series (parity contract). */
export function fetchStatsOverview(day: string, project?: string): Promise<StatsOverview> {
  if (MOCK) return mockApi.statsOverview(day);
  const qs = new URLSearchParams({ day });
  if (project !== undefined) qs.set('project', project);
  return get(`/api/stats/overview?${qs.toString()}`);
}

export function fetchHealth(): Promise<HealthResponse> {
  if (MOCK) return mockApi.health();
  return get('/api/health');
}

// --- analytics ----------------------------------------------------------------

/** Optional local-day range; the server defaults to the last 14 days. */
export interface AnalyticsRange {
  from?: string;
  to?: string;
  /** Global project scope — slug or id (server matches either). */
  project?: string;
}

function rangeQuery(range: AnalyticsRange, extra: Record<string, string>): string {
  const qs = new URLSearchParams(extra);
  if (range.from !== undefined) qs.set('from', range.from);
  if (range.to !== undefined) qs.set('to', range.to);
  if (range.project !== undefined) qs.set('project', range.project);
  return qs.toString();
}

/** Daily series for the main chart (one series per group member). */
export function fetchTimeseries(
  metric: AnalyticsMetric,
  group: AnalyticsDimension,
  range: AnalyticsRange = {},
): Promise<TimeseriesResp> {
  if (MOCK) return mockApi.timeseries(metric, group, range);
  return get(`/api/stats/timeseries?${rangeQuery(range, { metric, group })}`);
}

/** Ranked totals for the current pivot dimension. */
export function fetchBreakdown(
  by: AnalyticsDimension,
  range: AnalyticsRange = {},
): Promise<BreakdownResp> {
  if (MOCK) return mockApi.breakdown(by, range);
  return get(`/api/stats/breakdown?${rangeQuery(range, { by })}`);
}

/** Agents|skills × projects cross-tab (metric=runs, or cost for agents). */
export function fetchMatrix(
  rows: 'agent' | 'skill',
  metric: 'runs' | 'cost' = 'runs',
  range: AnalyticsRange = {},
): Promise<MatrixResp> {
  if (MOCK) return mockApi.matrix(rows, metric, range);
  return get(`/api/stats/matrix?${rangeQuery(range, { rows, cols: 'project', metric })}`);
}

/** Per-tool call/error/denied counts + duration stats (analytics uplift).
 * `agent` optionally narrows every row + column to one attributed agent. */
export function fetchToolStats(range: AnalyticsRange = {}, agent?: string): Promise<ToolsResp> {
  if (MOCK) return mockApi.toolStats(range, agent);
  return get(`/api/stats/tools?${rangeQuery(range, agent ? { agent } : {})}`);
}

/** Per-skill invocation/error/denied counts + duration stats (analytics uplift).
 * `agent` optionally narrows every row + column to one attributed agent. */
export function fetchSkillStats(range: AnalyticsRange = {}, agent?: string): Promise<SkillsResp> {
  if (MOCK) return mockApi.skillStats(range, agent);
  return get(`/api/stats/skills?${rangeQuery(range, agent ? { agent } : {})}`);
}

/** Session-duration + approval-wait aggregates (analytics uplift). */
export function fetchDurations(range: AnalyticsRange = {}): Promise<DurationsResp> {
  if (MOCK) return mockApi.durations(range);
  return get(`/api/stats/durations?${rangeQuery(range, {})}`);
}

/** Error events grouped by normalized message key (analytics uplift). */
export function fetchErrorGroups(range: AnalyticsRange = {}): Promise<ErrorsResp> {
  if (MOCK) return mockApi.errorGroups(range);
  return get(`/api/stats/errors?${rangeQuery(range, {})}`);
}

// --- analytics uplift (fusion phase 14) ---------------------------------------

/** Autonomy ratio — tool-calls per human intervention (fusion phase 14). */
export function fetchAutonomy(range: AnalyticsRange = {}): Promise<AutonomyResp> {
  if (MOCK) return mockApi.autonomy(range);
  return get(`/api/stats/autonomy?${rangeQuery(range, {})}`);
}

/** Productivity — commits/LOC/languages/durations + hours-saved ESTIMATE. */
export function fetchProductivity(range: AnalyticsRange = {}): Promise<ProductivityResp> {
  if (MOCK) return mockApi.productivity(range);
  return get(`/api/stats/productivity?${rangeQuery(range, {})}`);
}

/** SDLC funnel snapshot over the board columns (fusion phase 14). */
export function fetchFunnel(range: AnalyticsRange = {}): Promise<FunnelResp> {
  if (MOCK) return mockApi.funnel(range);
  return get(`/api/stats/funnel?${rangeQuery(range, {})}`);
}

/** Per-playbook rollup — empty list pre-Phase-13 (fusion phase 14). */
export function fetchPlaybookStats(range: AnalyticsRange = {}): Promise<PlaybookRollup[]> {
  if (MOCK) return mockApi.playbookStats(range);
  return get(`/api/stats/playbooks?${rangeQuery(range, {})}`);
}

/** Subscription-usage providers: the operator's LIVE Claude quota windows,
 * plus a telemetry-estimate card when SWARMERY_USAGE_LIMITS is set. Unscoped —
 * quota is global. `fresh` bypasses the daemon's 30s cache (the Refresh btn). */
export function fetchUsage(fresh = false): Promise<UsageResp> {
  if (MOCK) return mockApi.usage();
  return get(`/api/usage${fresh ? '?fresh=1' : ''}`);
}

/**
 * POST /api/usage/accounts/{account}/login/start — begin connecting an account
 * swarmery cannot read a credential for. Returns the URL to open in a browser;
 * the PKCE verifier and CSRF state stay in the daemon.
 */
export async function startUsageLogin(account: string): Promise<UsageLoginStart> {
  if (MOCK) throw new Error('connecting an account is not available in mock mode');
  const res = await fetch(`/api/usage/accounts/${encodeURIComponent(account)}/login/start`, {
    method: 'POST',
  });
  if (!res.ok) throw new Error(await errBody(res, 'could not start the connection'));
  return (await res.json()) as UsageLoginStart;
}

/**
 * POST /api/usage/accounts/{account}/login/complete — finish the connection with
 * the "code#state" value the callback page shows. The daemon exchanges it, stores
 * the credential, hands it over to the account's config dir, and runs the
 * readiness probe — the response reports all three outcomes (no token material
 * ever crosses back).
 */
export async function completeUsageLogin(account: string, code: string): Promise<UsageLoginComplete> {
  if (MOCK) throw new Error('connecting an account is not available in mock mode');
  const res = await fetch(`/api/usage/accounts/${encodeURIComponent(account)}/login/complete`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ code }),
  });
  if (!res.ok) throw new Error(await errBody(res, 'could not complete the connection'));
  return (await res.json()) as UsageLoginComplete;
}

/**
 * POST /api/accounts/{key}/probe — re-run the authoritative CLI-readiness probe
 * for one account. source='pty-login' records that the verdict follows an
 * interactive terminal login, keeping the stored verdict's provenance legible.
 */
export async function probeAccount(key: string, source?: 'pty-login'): Promise<AccountProbeResponse> {
  if (MOCK) throw new Error('probing an account is not available in mock mode');
  const qs = source !== undefined ? `?source=${source}` : '';
  const res = await fetch(`/api/accounts/${encodeURIComponent(key)}/probe${qs}`, {
    method: 'POST',
  });
  if (!res.ok) throw new Error(await errBody(res, 'could not re-check the account'));
  return (await res.json()) as AccountProbeResponse;
}

/**
 * DELETE /api/usage/accounts/{account}/login — disconnect an account.
 *
 * Removes the credential swarmery's own store holds for it, and nothing else:
 * the `claude` CLI's credential file and the macOS keychain item are untouched,
 * and nothing is revoked upstream at Anthropic. Idempotent — an account that is
 * already disconnected answers 200 too.
 */
export async function disconnectUsageAccount(account: string): Promise<void> {
  if (MOCK) throw new Error('disconnecting an account is not available in mock mode');
  const res = await fetch(`/api/usage/accounts/${encodeURIComponent(account)}/login`, {
    method: 'DELETE',
  });
  if (!res.ok) throw new Error(await errBody(res, 'could not disconnect the account'));
}

// --- retro loop (per-agent scorecards + friction board) -----------------------

/** Per-agent health scorecards + previous-window comparison (retro loop). */
export function fetchRetroAgents(range: AnalyticsRange = {}): Promise<RetroAgentsResp> {
  if (MOCK) return mockApi.retroAgents();
  return get(`/api/retro/agents?${rangeQuery(range, {})}`);
}

/** Friction board: denied tools, top error groups, approval waits (retro loop). */
export function fetchRetroFriction(range: AnalyticsRange = {}): Promise<RetroFrictionResp> {
  if (MOCK) return mockApi.retroFriction();
  return get(`/api/retro/friction?${rangeQuery(range, {})}`);
}

/** Lessons-learned feed parsed from 09-retrospective.md docs (retro phase 2). */
export function fetchRetroLessons(range: AnalyticsRange = {}): Promise<RetroLessonsResp> {
  if (MOCK) return mockApi.retroLessons();
  return get(`/api/retro/lessons?${rangeQuery(range, {})}`);
}

/** Estimation accuracy + loop/delegation counts per task (retro phase 2). */
export function fetchRetroTasks(range: AnalyticsRange = {}): Promise<RetroTasksResp> {
  if (MOCK) return mockApi.retroTasks();
  return get(`/api/retro/tasks?${rangeQuery(range, {})}`);
}

/**
 * GET /api/retro/report — every /retro section for one window plus the
 * deterministic markdown digest built from it. One call, one consistent
 * snapshot: the five per-section fetchers above can straddle an ingest tick
 * and disagree with each other, which is fine for a page and not for evidence.
 */
export function fetchRetroReport(range: AnalyticsRange = {}): Promise<RetroReportResp> {
  if (MOCK) return mockApi.retroReport();
  return get(`/api/retro/report?${rangeQuery(range, {})}`);
}

/**
 * The server's own `{"error": "…"}` sentence, falling back to the status code.
 * The improver card shows this text verbatim: an operator who is told "409"
 * learns nothing, and one who is told "accept it first" knows what to press.
 */
async function errText(res: Response): Promise<string> {
  const data = (await res.json().catch(() => ({}))) as { error?: string };
  return data.error ?? `request failed: ${String(res.status)}`;
}

/**
 * POST /api/retro/analysis — start the page-level improver over this window.
 * 202 with a `running` row; 409 when one is already in flight.
 */
export async function startRetroAnalysis(range: AnalyticsRange = {}): Promise<RetroAnalysis> {
  const res = await fetch(`/api/retro/analysis?${rangeQuery(range, {})}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
  });
  if (!res.ok) throw new Error(await errText(res));
  return (await res.json()) as RetroAnalysis;
}

/** GET /api/retro/analysis — the newest analysis for the scope, or null. */
export function fetchRetroAnalysis(project?: string): Promise<RetroAnalysisResp> {
  if (MOCK) return Promise.resolve({ analysis: null });
  const qs = project !== undefined && project !== '' ? `?project=${encodeURIComponent(project)}` : '';
  return get(`/api/retro/analysis${qs}`);
}

/** PATCH /api/retro/analysis/{id} — the operator's gate. */
export async function decideRetroAnalysis(
  id: number,
  status: 'accepted' | 'dismissed',
): Promise<RetroAnalysis> {
  const res = await fetch(`/api/retro/analysis/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  });
  if (!res.ok) throw new Error(await errText(res));
  return (await res.json()) as RetroAnalysis;
}

/**
 * POST /api/retro/analysis/{id}/plan — hand an accepted analysis to Planning
 * Mode. A 409 carries the ACTIVE session, so the caller rethrows a typed
 * conflict rather than a string the UI would have to parse.
 */
export async function planFromRetroAnalysis(
  id: number,
  projectId: number,
): Promise<RetroPlanStarted> {
  const res = await fetch(`/api/retro/analysis/${String(id)}/plan`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ projectId }),
  });
  if (res.status === 409) {
    const body = (await res.json().catch(() => ({}))) as Partial<RetroPlanConflict>;
    throw new RetroPlanConflictError(
      body.error ?? 'a planning run is already active for this project',
      body.sessionUuid ?? '',
      body.projectSlug ?? '',
    );
  }
  if (!res.ok) throw new Error(await errText(res));
  return (await res.json()) as RetroPlanStarted;
}

/** Typed 409 from planFromRetroAnalysis, carrying the active session to link to. */
export class RetroPlanConflictError extends Error {
  readonly sessionUuid: string;
  readonly projectSlug: string;
  constructor(message: string, sessionUuid: string, projectSlug: string) {
    super(message);
    this.name = 'RetroPlanConflictError';
    this.sessionUuid = sessionUuid;
    this.projectSlug = projectSlug;
  }
}

/**
 * Advisor recommendations (retro phase 3). `status` is a CSV filter or 'all';
 * the server defaults to the actionable set (proposed,accepted,adopted).
 */
export function fetchRecommendations(status?: string): Promise<RecommendationsResp> {
  if (MOCK) return mockApi.retroRecommendations();
  const qs = status !== undefined ? `?status=${encodeURIComponent(status)}` : '';
  return get(`/api/retro/recommendations${qs}`);
}

/**
 * PATCH /api/retro/recommendations/{id} — accept or dismiss one proposal.
 * Illegal transitions come back 422 with an {error} body.
 */
export async function patchRecommendation(
  id: number,
  status: Extract<RecommendationStatus, 'accepted' | 'dismissed'>,
): Promise<Recommendation> {
  if (MOCK) return mockApi.patchRecommendation(id, status);
  const res = await fetch(`/api/retro/recommendations/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `patch recommendation failed: ${String(res.status)}`);
  }
  return (await res.json()) as Recommendation;
}

/** POST /api/retro/advise — run the advisor rule engine now ("Analyze now"). */
export async function runAdvise(): Promise<AdviseStats> {
  if (MOCK) return mockApi.advise();
  const res = await fetch('/api/retro/advise', { method: 'POST' });
  if (!res.ok) throw new Error(`advise failed: ${String(res.status)}`);
  return (await res.json()) as AdviseStats;
}

// --- fusion phase 12 — project-scoped insights + memory ----------------------

/**
 * GET /api/retro/recommendations?projectId= — the advisor recs attributable to
 * one project (post-filtered on evidence session). `project` is a slug or id.
 */
export function fetchProjectRecommendations(
  project: string | number,
  status?: string,
): Promise<RecommendationsResp> {
  if (MOCK) return mockApi.projectRecommendations(project);
  const qs = new URLSearchParams({ projectId: String(project) });
  if (status !== undefined) qs.set('status', status);
  return get(`/api/retro/recommendations?${qs.toString()}`);
}

/**
 * POST /api/retro/advise?projectId= — run the advisor now for the project's
 * Insights card. The engine still runs fleet-wide (cross-project rates); the
 * projectId is accepted for API symmetry and the READ side does the narrowing.
 */
export async function runProjectAdvise(project: string | number): Promise<AdviseStats> {
  if (MOCK) return mockApi.advise();
  const qs = new URLSearchParams({ projectId: String(project) });
  const res = await fetch(`/api/retro/advise?${qs.toString()}`, { method: 'POST' });
  if (!res.ok) throw new Error(`advise failed: ${String(res.status)}`);
  return (await res.json()) as AdviseStats;
}

/** GET /api/projects/{id}/memory — the project's memory files across 3 roots. */
export function fetchMemoryList(project: string | number): Promise<MemoryListResp> {
  if (MOCK) return mockApi.memoryList(project);
  return get(`/api/projects/${encodeURIComponent(String(project))}/memory`);
}

/** GET /api/projects/{id}/memory/file?path= — one memory file's content+hash. */
export function fetchMemoryFile(
  project: string | number,
  path: string,
): Promise<MemoryFileContent> {
  if (MOCK) return mockApi.memoryFile(project, path);
  const qs = new URLSearchParams({ path });
  return get(`/api/projects/${encodeURIComponent(String(project))}/memory/file?${qs.toString()}`);
}

/**
 * PUT /api/projects/{id}/memory/file?path= — versioned write. A 409 (base_hash
 * drifted from disk) throws with the disk-side message so the editor can prompt
 * a reload; 403 means the readonly kill-switch is on.
 */
export async function putMemoryFile(
  project: string | number,
  path: string,
  content: string,
  baseHash: string,
): Promise<MemoryFileContent> {
  if (MOCK) return mockApi.putMemoryFile(project, path, content);
  const qs = new URLSearchParams({ path });
  const res = await fetch(
    `/api/projects/${encodeURIComponent(String(project))}/memory/file?${qs.toString()}`,
    {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content, base_hash: baseHash }),
    },
  );
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `save failed: ${String(res.status)}`);
  }
  return (await res.json()) as MemoryFileContent;
}

// --- self-improvement phase 4 — agent change proposals -----------------------

/** Extracts an {error} message from a failed response body, else a default. */
async function errBody(res: Response, fallback: string): Promise<string> {
  const data = (await res.json().catch(() => ({}))) as { error?: string };
  return data.error ?? `${fallback}: ${String(res.status)}`;
}

/** GET /api/retro/proposals — newest first; optional CSV status filter. */
export function fetchProposals(status?: string): Promise<ProposalsResp> {
  if (MOCK) return mockApi.proposals();
  const qs = status !== undefined ? `?status=${encodeURIComponent(status)}` : '';
  return get(`/api/retro/proposals${qs}`);
}

/**
 * GET /api/retro/agents/{agent}/evidence — read-only preview of the evidence
 * bundle the rewriter would feed the model. A built-in agent answers
 * in_registry:false with no bundle.
 */
export function fetchAgentEvidence(agent: string): Promise<AgentEvidence> {
  return get(`/api/retro/agents/${encodeURIComponent(agent)}/evidence`);
}

/**
 * POST /api/retro/agents/{agent}/improve — generate a proposal for one agent.
 * 202 while the model runs; 404 unknown agent; 409 an open proposal exists.
 */
export async function improveAgent(agent: string): Promise<void> {
  const res = await fetch(`/api/retro/agents/${encodeURIComponent(agent)}/improve`, {
    method: 'POST',
  });
  if (!res.ok) throw new Error(await errBody(res, 'improve agent failed'));
}

/**
 * PATCH /api/retro/proposals/{id} — approve or reject one proposal. Approving
 * fires the apply/PR pipeline async; illegal transitions come back 422.
 */
export async function patchProposal(
  id: number,
  status: 'approved' | 'rejected',
): Promise<void> {
  const res = await fetch(`/api/retro/proposals/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  });
  if (!res.ok) throw new Error(await errBody(res, 'decide proposal failed'));
}

/** POST /api/retro/proposals/{id}/retry — re-run generation for a failed row. */
export async function retryProposal(id: number): Promise<void> {
  const res = await fetch(`/api/retro/proposals/${String(id)}/retry`, { method: 'POST' });
  if (!res.ok) throw new Error(await errBody(res, 'retry proposal failed'));
}

/** POST /api/retro/proposals/{id}/apply — manual re-run of a stuck approved row. */
export async function applyProposal(id: number): Promise<void> {
  const res = await fetch(`/api/retro/proposals/${String(id)}/apply`, { method: 'POST' });
  if (!res.ok) throw new Error(await errBody(res, 'apply proposal failed'));
}

export function fetchDocs(): Promise<DocMeta[]> {
  if (MOCK) return mockApi.docs();
  return get('/api/docs');
}

export function fetchDoc(slug: string): Promise<DocDetail> {
  if (MOCK) return mockApi.doc(slug);
  return get(`/api/docs/${encodeURIComponent(slug)}`);
}

// --- phase 3.5: workspaces ----------------------------------------------------

/** Recently active workspace tasks (default window: 14 days). */
export function fetchTasks(days = 14): Promise<TasksResponse> {
  if (MOCK) return mockApi.tasks();
  return get(`/api/tasks?days=${String(days)}`);
}

/** One workspace task: card metadata + linked sessions + Σ cost. */
export function fetchTask(id: number | string): Promise<TaskDetail> {
  if (MOCK) return mockApi.task(id);
  return get(`/api/tasks/${encodeURIComponent(id)}`);
}

// --- fusion phase 1/3: task board + dispatcher (frozen contract in api/types) ---

/**
 * GET /api/board/tasks?projectId=&boardColumn= — dispatchable board rows
 * (source='queue'), newest first. Both filters optional; the board scopes by
 * projectId, the Archived column lazy-loads with boardColumn='archived'.
 */
export function fetchBoardTasks(projectId?: number, boardColumn?: BoardColumn): Promise<BoardTask[]> {
  if (MOCK) return mockApi.boardTasks(projectId, boardColumn);
  const qs = new URLSearchParams();
  if (projectId !== undefined) qs.set('projectId', String(projectId));
  if (boardColumn !== undefined) qs.set('boardColumn', boardColumn);
  const q = qs.toString();
  return get(`/api/board/tasks${q === '' ? '' : `?${q}`}`);
}

/** Body of POST /api/board/tasks (matches createBoardTask in tasks_board.go). */
export interface CreateBoardTaskInput {
  projectId: number;
  title: string;
  prompt: string;
  priority?: string;
  model?: string;
  /** Selected execution recipe name (fusion phase 13); omit/empty = default. */
  playbook?: string;
  /** Registry agent name to dispatch as; omit/empty = a plain run. */
  agent?: string;
  fileScope?: string[];
  /** Free-form card marks (0049), e.g. "jira-ticket"; server lowercases/trims/dedupes. */
  labels?: string[];
  dependencies?: string[];
  boardColumn?: BoardColumn;
}

/**
 * POST /api/board/tasks → 201 BoardTask (sent by the board's create modal).
 * A title-only intake sends prompt=title.
 */
export async function createBoardTask(input: CreateBoardTaskInput): Promise<BoardTask> {
  if (MOCK) return mockApi.createBoardTask(input);
  const res = await fetch('/api/board/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `create task failed: ${String(res.status)}`);
  }
  return (await res.json()) as BoardTask;
}

/**
 * User-editable subset of a board task (the fields patchBoardTask accepts —
 * boardColumn/title/prompt/priority/model/fileScope/dependencies/paused/
 * userPaused). Dispatcher-owned fields are NOT settable here.
 */
export interface PatchBoardTaskInput {
  boardColumn?: BoardColumn;
  title?: string;
  prompt?: string;
  priority?: string;
  model?: string | null;
  /** Selected recipe name; "" clears the selection back to the default. */
  playbook?: string | null;
  /** Registry agent name; "" clears the selection back to a plain run. */
  agent?: string | null;
  fileScope?: string[];
  dependencies?: string[];
  paused?: boolean;
  userPaused?: boolean;
}

/** PATCH /api/board/tasks/{id} → updated BoardTask (id = numeric row id). */
export async function patchBoardTask(id: number, patch: PatchBoardTaskInput): Promise<BoardTask> {
  if (MOCK) return mockApi.patchBoardTask(id, patch);
  const res = await fetch(`/api/board/tasks/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `patch task failed: ${String(res.status)}`);
  }
  return (await res.json()) as BoardTask;
}

/**
 * DELETE /api/board/tasks/{id} → 204. Permanent removal of a task that stopped
 * being relevant (Archive only parks it). A RUNNING task is refused with 409 —
 * the thrown message is the server's explanation, shown as-is in the UI.
 */
export async function deleteBoardTask(id: number): Promise<void> {
  if (MOCK) return mockApi.deleteBoardTask(id);
  const res = await fetch(`/api/board/tasks/${String(id)}`, { method: 'DELETE' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `delete task failed: ${String(res.status)}`);
  }
}

// --- board redesign phase 3: the review loop ---------------------------------

/**
 * Unwraps a review-action failure into an Error carrying the SERVER's sentence.
 *
 * The three exits answer with three different 4xx shapes — 409 {error, code},
 * 422 {error, hint, detail}, plain {error} — and every one of them is written to
 * be read by a human ("the branch is pushed, but `gh` is not on PATH… run this").
 * Flattening them to "request failed: 422" in the client would throw away the
 * only part of the response that tells the user what to do next.
 */
async function reviewActionError(res: Response, fallback: string): Promise<Error> {
  const body = (await res.json().catch(() => ({}))) as {
    error?: string;
    hint?: string;
    detail?: string;
  };
  const parts = [body.error ?? `${fallback}: ${String(res.status)}`];
  if (body.hint !== undefined && body.hint !== '') parts.push(body.hint);
  if (body.detail !== undefined && body.detail !== '') parts.push(body.detail);
  return new Error(parts.join('\n\n'));
}

/**
 * GET /api/board/tasks/{id}/diff — the commits, file stats and unified patch of
 * a card's run branch. 409 when the card was never dispatched or has no pinned
 * base; 404 when the branch was deleted out of band.
 */
export async function getBoardTaskDiff(id: number): Promise<TaskDiff> {
  const res = await fetch(`/api/board/tasks/${String(id)}/diff`);
  if (!res.ok) throw await reviewActionError(res, 'diff failed');
  return (await res.json()) as TaskDiff;
}

/**
 * POST /api/tasks/{id}/verify → 202. Re-grade a card. NOT a board route: the
 * manual verify trigger predates the review loop and already carries the right
 * preflight (404 unknown / 422 no worktree / 409 already running / 503 verifier
 * not attached), so the review UI calls it rather than a board-scoped twin.
 *
 * 202 means "started" — the verdict lands later on a task_updated frame.
 */
export async function verifyBoardTask(id: number): Promise<void> {
  const res = await fetch(`/api/tasks/${String(id)}/verify`, { method: 'POST' });
  if (!res.ok) throw await reviewActionError(res, 'verify failed');
}

/**
 * POST /api/board/tasks/{id}/rerun — append the reviewer's feedback to the
 * card's prompt and send it back to todo. 409 outside in_review/done.
 */
export async function rerunBoardTask(id: number, feedback: string): Promise<BoardTask> {
  const res = await fetch(`/api/board/tasks/${String(id)}/rerun`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ feedback }),
  });
  if (!res.ok) throw await reviewActionError(res, 'rerun failed');
  return (await res.json()) as BoardTask;
}

/**
 * POST /api/board/tasks/{id}/discard — reclaim the worktree, delete the run
 * branch AND its commits, archive the card. Idempotent: a branch already gone
 * still succeeds with `deleted: false`. 409 while the card is running.
 */
export async function discardBoardTask(id: number): Promise<DiscardTaskResponse> {
  const res = await fetch(`/api/board/tasks/${String(id)}/discard`, { method: 'POST' });
  if (!res.ok) throw await reviewActionError(res, 'discard failed');
  return (await res.json()) as DiscardTaskResponse;
}

/**
 * POST /api/board/tasks/{id}/land — push the run branch, open a PR for it, and
 * move the card to done. 422 (with a `hint` naming the exact manual commands)
 * when the repo has no origin or `gh` is not installed.
 */
export async function landBoardTask(id: number, draft = false): Promise<LandTaskResponse> {
  const res = await fetch(`/api/board/tasks/${String(id)}/land`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ draft }),
  });
  if (!res.ok) throw await reviewActionError(res, 'land failed');
  return (await res.json()) as LandTaskResponse;
}

/**
 * Body of POST /api/board/tasks/bulk-archive — the inbox amnesty. The server
 * applies the same eligibility predicate as the TTL sweeper (captured origin,
 * still in Triage, no worktree), so this body only ever NARROWS it.
 */
export interface BulkArchiveInput {
  /** Omit to sweep every project. */
  projectId?: number;
  /**
   * Only 'triage' is accepted. It tells the server nothing it could not assume —
   * that is the point: naming the column means a column added later can never be
   * swept by a client written before it existed.
   */
  column: 'triage';
  /** RFC3339 cutoff — cards idle since before this instant are eligible. */
  before: string;
  /** Count the matches without writing anything (drives the confirm step). */
  dryRun?: boolean;
}

/** Response of the amnesty: `archived` is 0 on a dry run, `matched` otherwise. */
export interface BulkArchiveResult {
  matched: number;
  archived: number;
}

/**
 * POST /api/board/tasks/bulk-archive → {matched, archived}. Call it once with
 * `dryRun: true` to show the blast radius, then again with the SAME body minus
 * the flag to commit — a bulk archive has no undo, so the count the user
 * approved and the write that follows must come from one predicate.
 *
 * Emits no WS frames by design: the caller reloads its own board off this
 * response, other tabs converge on the next reconcile tick.
 */
export async function bulkArchiveBoardTasks(input: BulkArchiveInput): Promise<BulkArchiveResult> {
  if (MOCK) return { matched: 0, archived: 0 }; // no-op in mock mode
  const res = await fetch('/api/board/tasks/bulk-archive', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `bulk archive failed: ${String(res.status)}`);
  }
  return (await res.json()) as BulkArchiveResult;
}

/** GET /api/dispatch — dispatcher status snapshot (503 when not attached). */
export function fetchDispatchStatus(): Promise<DispatchStatus> {
  if (MOCK) return mockApi.dispatch();
  return get('/api/dispatch');
}

// --- fusion phase 13: playbooks (selectable workflows) ------------------------

/**
 * GET /api/playbooks?projectId= — the playbooks visible to a project (built-ins
 * overlaid by the project's own .claude/playbooks files), sorted by name. Omit
 * projectId for built-ins only.
 */
export function fetchPlaybooks(projectId?: number): Promise<Playbook[]> {
  if (MOCK) return mockApi.playbooks(projectId);
  const qs = projectId !== undefined ? `?projectId=${String(projectId)}` : '';
  return get(`/api/playbooks${qs}`);
}

/**
 * POST /api/projects/{id}/playbooks/{name}/duplicate — copy a built-in's
 * markdown into the project so its prompts become editable. Non-2xx (404 unknown
 * built-in/project, 409 the project file already exists, 503 not attached)
 * throws the server's {error} text for inline display.
 */
export async function duplicatePlaybook(
  projectId: number,
  name: string,
): Promise<DuplicatePlaybookResponse> {
  if (MOCK) return mockApi.duplicatePlaybook(projectId, name);
  const res = await fetch(
    `/api/projects/${String(projectId)}/playbooks/${encodeURIComponent(name)}/duplicate`,
    { method: 'POST' },
  );
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `duplicate playbook failed: ${String(res.status)}`);
  }
  return (await res.json()) as DuplicatePlaybookResponse;
}

// --- fusion phase 8: planning mode --------------------------------------------

/** GET /api/projects/{id}/planning — the planner status for a project. */
export function fetchPlanning(projectId: number): Promise<PlanningStatus> {
  if (MOCK) return mockApi.planning(projectId);
  return get(`/api/projects/${String(projectId)}/planning`);
}

/**
 * POST /api/projects/{id}/planning {idea} — spawn a headless planner run.
 * Returns 202 with the pre-generated session uuid. Non-2xx (400 empty idea,
 * 404 unknown project, 409 a run is already active, 503 not attached) throws the
 * server's {error} text for inline display.
 */
/** model is a planning short name (`opus` | `sonnet` | `fable`) or full ID;
 * omit it for the planner default. 400 on an unknown model. */
export async function startPlanning(projectId: number, idea: string, model?: string): Promise<PlanningStart> {
  if (MOCK) return mockApi.startPlanning(projectId, idea, model);
  const res = await fetch(`/api/projects/${String(projectId)}/planning`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(model !== undefined && model !== '' ? { idea, model } : { idea }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `start planning failed: ${String(res.status)}`);
  }
  return (await res.json()) as PlanningStart;
}

/** POST /api/projects/{id}/planning/cancel — abort the in-flight planner run. */
export async function cancelPlanning(projectId: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(projectId)}/planning/cancel`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `cancel planning failed: ${String(res.status)}`);
  }
}

/**
 * POST /api/projects/{id}/planning/answer — consume the current wizard
 * question. Structured mode sends the question id + selected option ids
 * (otherText fills the "Other" option); raw-fallback mode sends questionId ""
 * with empty selectedOptionIds and the whole free-text reply as otherText.
 * Non-2xx (400 empty selection, 404 no wizard, 409 not awaiting / wrong
 * question / resume in flight) throws the server's {error} text.
 */
export async function answerPlanning(
  projectId: number,
  body: { questionId: string; selectedOptionIds: string[]; otherText?: string },
): Promise<{ status: string }> {
  if (MOCK) return mockApi.answerPlanning(projectId, body);
  const res = await fetch(`/api/projects/${String(projectId)}/planning/answer`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `answer planning failed: ${String(res.status)}`);
  }
  return (await res.json()) as { status: string };
}

/**
 * POST /api/projects/{id}/planning/refine {instructions} — free-form
 * course-correction: the plan updates and the next questions follow the
 * operator's direction. Same error matrix as answer.
 */
export async function refinePlanning(
  projectId: number,
  instructions: string,
): Promise<{ status: string }> {
  if (MOCK) return mockApi.refinePlanning(projectId, instructions);
  const res = await fetch(`/api/projects/${String(projectId)}/planning/refine`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ instructions }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `refine planning failed: ${String(res.status)}`);
  }
  return (await res.json()) as { status: string };
}

/**
 * POST /api/projects/{id}/planning/proceed — end the interview and trigger
 * plan writing (PHASE B). 404/409 as answer.
 */
export async function proceedPlanning(projectId: number): Promise<{ status: string }> {
  if (MOCK) return mockApi.proceedPlanning(projectId);
  const res = await fetch(`/api/projects/${String(projectId)}/planning/proceed`, {
    method: 'POST',
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `proceed planning failed: ${String(res.status)}`);
  }
  return (await res.json()) as { status: string };
}

// --- plan revisions (plan-revision phase 4) -----------------------------------

/** startRevision 409: when a staged revision is already open, the server names
 * it so the UI can offer "review it" instead of a dead end. */
export interface RevisionStartError extends Error {
  revisionId?: number;
}

/** applyRevision 409: every file whose live content drifted since staging. */
export interface RevisionApplyError extends Error {
  conflicts?: RevisionConflict[];
}

/**
 * POST /api/epics/{taskId}/revisions {reason, phaseId?} → 202 {sessionUuid} — a
 * revise wizard against the task's existing plan. Non-2xx (400 empty reason,
 * 404 unknown task, 409 plan busy / revision open / planner active, 503 not
 * attached) throws the server's {error}; the revision-open 409 carries the open
 * revision's id on the thrown error (RevisionStartError).
 */
export async function startRevision(
  taskId: number,
  reason: string,
  phaseId?: number,
): Promise<PlanningStart> {
  if (MOCK) return mockApi.startRevision(taskId, reason, phaseId);
  const res = await fetch(`/api/epics/${String(taskId)}/revisions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ reason, ...(phaseId !== undefined ? { phaseId } : {}) }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string; revisionId?: number };
    const err: RevisionStartError = new Error(
      data.error ?? `start revision failed: ${String(res.status)}`,
    );
    if (typeof data.revisionId === 'number') err.revisionId = data.revisionId;
    throw err;
  }
  return (await res.json()) as PlanningStart;
}

/** GET /api/epics/{taskId}/revisions — every revision of the task's plan,
 * newest first (file actions only, no diffs). */
export async function fetchRevisions(taskId: number): Promise<PlanRevision[]> {
  if (MOCK) return mockApi.revisions(taskId);
  const data = await get<{ revisions: PlanRevision[] }>(
    `/api/epics/${String(taskId)}/revisions`,
  );
  return data.revisions;
}

/** GET /api/revisions/{id} — one revision with per-file `stale` flags and
 * unified diffs rendered against the LIVE docs at request time. */
export async function fetchRevision(revisionId: number): Promise<PlanRevision> {
  if (MOCK) return mockApi.revision(revisionId);
  const data = await get<{ revision: PlanRevision; files: RevisionFile[] }>(
    `/api/revisions/${String(revisionId)}`,
  );
  return { ...data.revision, files: data.files };
}

/**
 * POST /api/revisions/{id}/apply → 200 {status:"applied", files:N} — the one
 * irreversible step. A 409 (content drifted since staging) throws an error
 * whose MESSAGE names the conflicting docs and whose `conflicts` carries the
 * full rows (RevisionApplyError), so the review view can render them.
 */
export async function applyRevision(
  revisionId: number,
): Promise<{ status: string; files: number }> {
  if (MOCK) return mockApi.applyRevision(revisionId);
  const res = await fetch(`/api/revisions/${String(revisionId)}/apply`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as {
      error?: string;
      conflicts?: RevisionConflict[];
    };
    const docs = (data.conflicts ?? []).map((c) => c.docPath);
    const err: RevisionApplyError = new Error(
      docs.length > 0
        ? `${data.error ?? 'apply conflicts'}: ${docs.join(', ')}`
        : (data.error ?? `apply revision failed: ${String(res.status)}`),
    );
    if (data.conflicts !== undefined) err.conflicts = data.conflicts;
    throw err;
  }
  return (await res.json()) as { status: string; files: number };
}

/** POST /api/revisions/{id}/reject {note?} → 200 — decline the staged diff; no
 * plan file changes. The note lands on the revision's reason ("Rejected: …"). */
export async function rejectRevision(revisionId: number, note?: string): Promise<void> {
  if (MOCK) return mockApi.rejectRevision(revisionId, note);
  const res = await fetch(`/api/revisions/${String(revisionId)}/reject`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(note !== undefined && note.trim() !== '' ? { note } : {}),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `reject revision failed: ${String(res.status)}`);
  }
}

/** POST /api/dispatch/pause — global or per-project pause toggle. */
export async function pauseDispatch(
  scope: 'global' | 'project',
  paused: boolean,
  projectId?: number,
): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const body: { scope: string; paused: boolean; projectId?: number } = { scope, paused };
  if (scope === 'project' && projectId !== undefined) body.projectId = projectId;
  const res = await fetch('/api/dispatch/pause', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `pause failed: ${String(res.status)}`);
  }
}

// --- phase 2 — approvals (docs/hooks-protocol.md; DTO frozen in api/types.ts) ---

/**
 * `resolved` is a meta-filter covering every terminal status — assumed
 * server-side (see web/CONTRACT-REQUESTS.md); the UI only ever asks for
 * `pending` and `resolved`.
 */
export type ApprovalStatusFilter = PermissionRequestStatus | 'resolved';

/**
 * `answer` resolves an AskUserQuestion with per-question answers; `terminal`
 * is the no-decision handoff to the native terminal selector (E12d/E12e —
 * a plain approve would resolve the questions unanswered).
 */
export type ApprovalAction = 'approve' | 'deny' | 'answer' | 'terminal';

/** {action:"answer"} answers: string, or an array of labels for multiSelect. */
export type ApprovalAnswers = Record<string, string | string[]>;

export function fetchApprovals(
  status?: ApprovalStatusFilter,
  project?: string | null,
): Promise<PermissionRequest[]> {
  if (MOCK) return mockApi.approvals(status, project);
  const qs = new URLSearchParams();
  if (status !== undefined) qs.set('status', status);
  if (project !== undefined && project !== null && project !== '') qs.set('project', project);
  const query = qs.toString();
  return get(`/api/approvals${query === '' ? '' : `?${query}`}`);
}

/**
 * POST /api/approvals/{id} → 200 with the updated PermissionRequest.
 * Non-2xx (e.g. 409 when the row raced to a terminal state via the terminal
 * dialog or expiry) throws — callers silently refetch; the WS
 * permission_resolved is the authoritative reconciliation either way.
 */
export async function resolveApproval(
  id: number,
  action: ApprovalAction,
  reason?: string,
  answers?: ApprovalAnswers,
): Promise<PermissionRequest> {
  if (MOCK) return mockApi.resolveApproval(id, action, reason, answers);
  const body: { action: ApprovalAction; reason?: string; answers?: ApprovalAnswers } = { action };
  if (reason !== undefined && reason !== '') body.reason = reason;
  if (answers !== undefined) body.answers = answers;
  const res = await fetch(`/api/approvals/${String(id)}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`POST /api/approvals/${String(id)}: ${String(res.status)}`);
  }
  return (await res.json()) as PermissionRequest;
}

// --- control-plane v2 — auto-approve rules ------------------------------------

export interface ApprovalRuleInput {
  projectId: number | null;
  toolPattern: string;
  note?: string;
}

export function fetchApprovalRules(): Promise<ApprovalRule[]> {
  if (MOCK) return Promise.resolve([]);
  return get('/api/approval-rules');
}

export async function createApprovalRule(input: ApprovalRuleInput): Promise<ApprovalRule> {
  const res = await fetch('/api/approval-rules', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `create rule failed: ${String(res.status)}`);
  }
  return (await res.json()) as ApprovalRule;
}

export async function toggleApprovalRule(id: number, enabled: boolean): Promise<ApprovalRule> {
  const res = await fetch(`/api/approval-rules/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ enabled }),
  });
  if (!res.ok) throw new Error(`toggle rule failed: ${String(res.status)}`);
  return (await res.json()) as ApprovalRule;
}

export async function deleteApprovalRule(id: number): Promise<void> {
  const res = await fetch(`/api/approval-rules/${String(id)}`, { method: 'DELETE' });
  if (!res.ok) throw new Error(`delete rule failed: ${String(res.status)}`);
}

// --- fusion phase 11 — permission presets ------------------------------------

/**
 * Thrown by putPermissionPreset on a 428: the change escalates privileges and
 * needs explicit confirmation. Carries the escalation list so the caller can
 * render a confirm dialog, then retry with `confirm: true`.
 */
export class EscalationRequiredError extends Error {
  readonly escalations: string[];
  readonly reason: string;
  constructor(payload: PermissionEscalation) {
    super(payload.error);
    this.name = 'EscalationRequiredError';
    this.escalations = payload.escalations;
    this.reason = payload.reason;
  }
}

export function fetchPermissionPreset(projectId: number | string): Promise<PermissionPresetView> {
  return get(`/api/projects/${String(projectId)}/permission-preset`);
}

/**
 * PUT /api/projects/{id}/permission-preset. On a 428 (privileged change without
 * confirm) throws {@link EscalationRequiredError}; other non-2xx throw a plain
 * Error. Returns the recompiled effective policy view on success.
 */
export async function putPermissionPreset(
  projectId: number | string,
  input: PermissionPresetInput,
): Promise<PermissionPresetView> {
  const res = await fetch(`/api/projects/${String(projectId)}/permission-preset`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (res.status === 428) {
    const payload = (await res.json().catch(() => ({
      error: 'confirmation required',
      reason: '',
      escalations: [],
    }))) as PermissionEscalation;
    throw new EscalationRequiredError(payload);
  }
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `set preset failed: ${String(res.status)}`);
  }
  return (await res.json()) as PermissionPresetView;
}

/**
 * POST /api/sessions/{id}/extract-tasks — run one model pass over a session and
 * put the tasks it names on the board as suggested Triage cards (origin='llm').
 *
 * Resolves with the number of cards REALLY inserted, which is what the caller
 * shows: a re-run over an unchanged session is idempotent and legitimately
 * reports 0. Rejects with the server's own detail on 409 (the session cannot
 * produce cards, or a run is already in flight) and 502 (the model answered
 * something unusable) — both are things the operator needs to read, not a
 * silent zero.
 *
 * Slow by nature: the request is held for the whole headless run (bounded
 * server-side at 5 minutes), so callers must show a pending state.
 */
export async function extractSessionTasks(id: number): Promise<number> {
  if (MOCK) return 0; // no-op in mock mode — never spend tokens from a demo
  const res = await fetch(`/api/sessions/${String(id)}/extract-tasks`, { method: 'POST' });
  const data = (await res.json().catch(() => ({}))) as { error?: string; inserted?: number };
  if (!res.ok) {
    throw new Error(data.error ?? `extract failed: ${String(res.status)}`);
  }
  return data.inserted ?? 0;
}

/** POST /api/sessions/{id}/kill — send SIGTERM (force=false) or SIGKILL (force=true). */
export async function killSession(id: number, force = false): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}/kill`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ force }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string };
    throw new Error(data.error ?? `kill failed: ${String(res.status)}`);
  }
}

/** POST /api/sessions/{id}/stop — graceful SIGTERM; the session is recorded
 * as 'completed' (not 'killed'). Works even when no PID is known. */
export async function stopSession(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}/stop`, { method: 'POST' });
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string };
    throw new Error(data.error ?? `stop failed: ${String(res.status)}`);
  }
}

/**
 * POST /api/sessions/{id}/message — resume an idle/completed conversation
 * headlessly (`claude -r <uuid> -p <text>`). Returns 202 immediately; the
 * resulting user + assistant turns arrive on the open detail via the WS bus
 * once the ingest watcher tails the transcript. Non-2xx (409 for a live
 * session or for a session whose working directory is gone — a run's worktree
 * is removed when the run ends — and 503 when the claude binary is missing)
 * throws with the server's error text so the composer can surface it inline.
 */
export async function sendSessionMessage(id: number, text: string): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}/message`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text }),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string };
    throw new Error(data.error ?? `send failed: ${String(res.status)}`);
  }
}

/**
 * POST /api/sessions/{id}/message/cancel — abort the in-flight headless resume
 * run (kills the child claude process). 409 when nothing is in flight.
 */
export async function cancelSessionMessage(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}/message/cancel`, { method: 'POST' });
  if (!res.ok) {
    const data = await res.json().catch(() => ({})) as { error?: string };
    throw new Error(data.error ?? `cancel failed: ${String(res.status)}`);
  }
}

/**
 * PATCH /api/sessions/{id} — set or clear (null) the manual session outcome.
 */
export async function patchSessionOutcome(
  id: number,
  outcome: SessionOutcome | null,
): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ outcome }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `outcome failed: ${String(res.status)}`);
  }
}

/**
 * PATCH /api/sessions/{id} — rename a session. A blank/null title clears the
 * override and reverts to the ingested ai-title.
 */
export async function renameSession(id: number, title: string | null): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/sessions/${String(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `rename failed: ${String(res.status)}`);
  }
}

// --- tool dashboards (serena LSP dashboard + graphify viz) ----------------------

/** GET /api/tools — sidebar feed for daemon-managed tool dashboards. */
export function fetchTools(): Promise<ToolsResponse> {
  if (MOCK) return mockApi.tools();
  return get('/api/tools');
}

/**
 * POST /api/projects/{id}/serena/start — launch the project's serena dashboard
 * process. Non-2xx (403 fence closed, 404 no lsp-pack, 409 already running,
 * 503 binary missing) throws the server's {error} text for inline display.
 */
export async function serenaStart(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}/serena/start`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `serena start failed: ${String(res.status)}`);
  }
}

/** POST /api/projects/{id}/serena/stop — stop the dashboard process. */
export async function serenaStop(id: number): Promise<void> {
  if (MOCK) return; // no-op in mock mode
  const res = await fetch(`/api/projects/${String(id)}/serena/stop`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `serena stop failed: ${String(res.status)}`);
  }
}

// --- connectors (MCP servers) --------------------------------------------------

/**
 * Thrown when the daemon cannot serve connectors at all (HTTP 503): the reader
 * is not attached, or the `claude` CLI is not findable from the daemon's PATH.
 * Distinct from a plain Error so the UI can degrade to a muted "unavailable"
 * card instead of a red failure — this is a host condition, not a bug.
 */
export class ConnectorsUnavailableError extends Error {
  /** The daemon's actionable remedy, when it sent one. */
  readonly hint: string | null;

  constructor(message: string, hint: string | null) {
    super(message);
    this.name = 'ConnectorsUnavailableError';
    this.hint = hint;
  }
}

/**
 * GET /api/connectors. Deliberately does NOT use the shared `get<T>` helper:
 * that one discards the response body, and the 503 body's `error`/`hint` is the
 * whole point — it is what tells the operator how to fix an unfindable CLI.
 */
export async function fetchConnectors(): Promise<ConnectorsResponse> {
  if (MOCK) return mockApi.connectors();
  const res = await fetch('/api/connectors');
  if (res.status === 503) {
    const data = (await res.json().catch(() => ({}))) as { error?: string; hint?: string };
    throw new ConnectorsUnavailableError(data.error ?? 'connectors unavailable', data.hint ?? null);
  }
  if (!res.ok) {
    throw new Error(`GET /api/connectors: ${String(res.status)}`);
  }
  return (await res.json()) as ConnectorsResponse;
}

/** Add a stdio/http/sse MCP server; the daemon returns the refreshed list. */
export async function addConnector(input: AddConnectorInput): Promise<Connector[]> {
  if (MOCK) return (await mockApi.connectors()).connectors;
  const res = await fetch('/api/connectors', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `add connector failed: ${String(res.status)}`);
  }
  const body = (await res.json()) as ConnectorsResponse;
  return body.connectors;
}

/** Remove a server by name (optionally scoped); returns the refreshed list. */
export async function removeConnector(name: string, scope?: string): Promise<Connector[]> {
  if (MOCK) return (await mockApi.connectors()).connectors;
  const qs = scope !== undefined && scope !== '' ? `?scope=${encodeURIComponent(scope)}` : '';
  const res = await fetch(`/api/connectors/${encodeURIComponent(name)}${qs}`, { method: 'DELETE' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `remove connector failed: ${String(res.status)}`);
  }
  const body = (await res.json()) as ConnectorsResponse;
  return body.connectors;
}

// --- accounts (multi-account, phase 7) ---------------------------------------

export function fetchAccounts(): Promise<AccountsResponse> {
  if (MOCK) return mockApi.accounts();
  return get('/api/accounts');
}

export async function createAccount(key: string): Promise<ProvisionResponse> {
  if (MOCK) return mockApi.createAccount(key);
  const res = await fetch('/api/accounts', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ key }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `create account failed: ${String(res.status)}`);
  }
  return (await res.json()) as ProvisionResponse;
}

export async function deleteAccount(key: string): Promise<RemoveAccountResponse> {
  if (MOCK) return mockApi.deleteAccount(key);
  const res = await fetch(`/api/accounts/${encodeURIComponent(key)}`, { method: 'DELETE' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `delete account failed: ${String(res.status)}`);
  }
  return (await res.json()) as RemoveAccountResponse;
}

export function fetchProjectAccount(id: number | string): Promise<AccountBinding> {
  if (MOCK) return mockApi.projectAccount(id);
  return get(`/api/projects/${encodeURIComponent(id)}/account`);
}

export async function putProjectAccount(
  id: number | string,
  account: string,
): Promise<AccountBinding> {
  if (MOCK) return mockApi.putProjectAccount(id, account);
  const res = await fetch(`/api/projects/${encodeURIComponent(id)}/account`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ account }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `set account failed: ${String(res.status)}`);
  }
  return (await res.json()) as AccountBinding;
}

// --- global search (Cmd+K palette) ---------------------------------------------

/**
 * GET /api/search — grouped global search (sessions / turns / files /
 * projects). Mock mode returns empty groups so the palette still renders its
 * static Navigation section offline.
 */
export function fetchSearch(q: string, project?: string, limit = 20): Promise<SearchResponse> {
  if (MOCK) {
    return Promise.resolve({ query: q, sessions: [], turns: [], files: [], projects: [] });
  }
  const qs = new URLSearchParams({ q, limit: String(limit) });
  if (project !== undefined && project !== '') qs.set('project', project);
  return get(`/api/search?${qs.toString()}`);
}

/** GET /api/files/sessions — sessions that touched files matching `path`. */
export function fetchFileSessions(path: string, project?: string): Promise<FileSessionsResponse> {
  if (MOCK) return Promise.resolve({ path, sessions: [] });
  const qs = new URLSearchParams({ path });
  if (project !== undefined && project !== '') qs.set('project', project);
  return get(`/api/files/sessions?${qs.toString()}`);
}

// ── Routines (fusion phase 7) ───────────────────────────────────────────────

/** GET /api/routines — all routines (optionally project-scoped), newest first. */
export function fetchRoutines(projectId?: number): Promise<Routine[]> {
  if (MOCK) return mockApi.routines();
  const qs = projectId ? `?projectId=${String(projectId)}` : '';
  return get(`/api/routines${qs}`);
}

/** GET /api/routines/{id}/runs — run history (newest first). */
export function fetchRoutineRuns(id: string): Promise<RoutineRun[]> {
  if (MOCK) return mockApi.routineRuns(id);
  return get(`/api/routines/${encodeURIComponent(id)}/runs`);
}

/** POST /api/routines — create. Returns the routine (with webhookToken when
 * webhook:true was requested). */
export async function createRoutine(input: RoutineInput): Promise<Routine> {
  if (MOCK) return mockApi.createRoutine(input);
  const res = await fetch('/api/routines', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `create routine failed: ${String(res.status)}`);
  }
  return (await res.json()) as Routine;
}

/** PATCH /api/routines/{id} — partial update. */
export async function patchRoutine(id: string, input: Partial<RoutineInput>): Promise<Routine> {
  if (MOCK) return mockApi.patchRoutine(id, input);
  const res = await fetch(`/api/routines/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `update routine failed: ${String(res.status)}`);
  }
  return (await res.json()) as Routine;
}

/** DELETE /api/routines/{id}. */
export async function deleteRoutine(id: string): Promise<void> {
  if (MOCK) return;
  const res = await fetch(`/api/routines/${encodeURIComponent(id)}`, { method: 'DELETE' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `delete routine failed: ${String(res.status)}`);
  }
}

/** POST /api/routines/{id}/run — manual trigger. Returns whether a run started
 * (false when the routine is already running / the global cap is full). */
export async function runRoutine(id: string): Promise<{ status: string }> {
  if (MOCK) return { status: 'started' };
  const res = await fetch(`/api/routines/${encodeURIComponent(id)}/run`, { method: 'POST' });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `run routine failed: ${String(res.status)}`);
  }
  return (await res.json()) as { status: string };
}

// ── Epics (fusion phase 10) ─────────────────────────────────────────────────

/** GET /api/epics?projectId= — epics (workspace plans) with phases + rollups. */
export function fetchEpics(projectId?: number): Promise<Epic[]> {
  if (MOCK) return mockApi.epics(projectId);
  const qs = projectId !== undefined ? `?projectId=${String(projectId)}` : '';
  return get(`/api/epics${qs}`);
}

export type EpicLifecycleAction = 'pause' | 'resume' | 'archive' | 'restore';

/**
 * POST /api/epics/{taskId}/lifecycle {action} → 200 {status}. File-backed on
 * the daemon side (README status rewrite / working↔archive zone move); 409 on
 * an invalid transition, 404 when the task has no plan dir.
 */
export async function epicLifecycle(
  taskId: number,
  action: EpicLifecycleAction,
): Promise<{ status: Epic['status'] }> {
  if (MOCK) {
    const next: Record<EpicLifecycleAction, Epic['status']> = {
      pause: 'paused',
      resume: 'active',
      archive: 'archived',
      restore: 'active',
    };
    return { status: next[action] };
  }
  const res = await fetch(`/api/epics/${String(taskId)}/lifecycle`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action }),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `lifecycle ${action} failed (${String(res.status)})`);
  }
  return (await res.json()) as { status: Epic['status'] };
}

/**
 * A run rejection that carries structured escape-hatch data. The
 * branch-holds-commits 409 names the branch (and how far ahead it is) so the UI
 * can offer "delete it and retry" instead of asking the user to parse prose.
 *
 * Both run surfaces throw this: the phase run and the whole-plan run answer with
 * the same four fields, so one type covers both. `base` names the branch
 * commitsAhead was measured against — without it "2 commits ahead" is not a fact
 * the user can act on, since the same branch can be 2 ahead of `dev` and 0 ahead
 * of a feature branch that already carries them. Absent when the daemon could not
 * name a base (no git seam, detached HEAD) — never guessed.
 */
export type PhaseRunBranchError = Error & {
  /**
   * The 409's stable discriminator. Callers switch on THIS, never on which
   * fields happen to be present: a body-shape sniff mis-classifies every future
   * case that carries the same field. Undefined only for a non-409 failure (or a
   * body the daemon could not encode), so a consumer must treat "no code" as
   * "unknown rejection", not as any particular case.
   */
  code?: RunConflictCode | undefined;
  branch?: string | undefined;
  commitsAhead?: number | undefined;
  base?: string | undefined;
};

/**
 * runConflictError builds the enriched error every run/branch endpoint throws.
 *
 * The branch facts ride along only for the case that actually measured them
 * (`branch-dirty`), keyed off `code` rather than off `body.branch !== undefined`
 * — the sniff this helper exists to replace. Every other 409 still gets its
 * `code`, so a caller can distinguish "checked out elsewhere" from "is HEAD"
 * from "outside swarm/" without reading the prose.
 */
function runConflictError(
  body: {
    error?: string;
    code?: RunConflictCode;
    branch?: string;
    commitsAhead?: number;
    base?: string;
  },
  fallback: string,
): PhaseRunBranchError {
  const err: PhaseRunBranchError = new Error(body.error ?? fallback);
  if (body.code !== undefined) err.code = body.code;
  if (body.code === 'branch-dirty') {
    err.branch = body.branch;
    err.commitsAhead = body.commitsAhead;
    err.base = body.base;
  }
  return err;
}

/**
 * POST /api/epics/{taskId}/phases/{phaseId}/run — execute one plan phase
 * headlessly in an isolated worktree (no board task). 202 {status, sessionUuid};
 * 409 carries the gate reason (already running / unmet deps / no doc) in the
 * error body — surfaced verbatim for the toast.
 */
export async function runEpicPhase(
  taskId: number,
  phaseId: number,
): Promise<{ status: string; sessionUuid: string }> {
  if (MOCK) return { status: 'running', sessionUuid: 'mock-run-uuid' };
  const res = await fetch(`/api/epics/${String(taskId)}/phases/${String(phaseId)}/run`, {
    method: 'POST',
  });
  if (!res.ok) {
    // Every 409 carries a `code`; the branch-holds-commits one additionally
    // carries structured escape-hatch data (`branch`, `commitsAhead`, `base`) the
    // caller turns into a "delete the branch" affordance. Both ride along on the
    // Error rather than being flattened into the message, which would force the
    // UI to parse prose.
    const body = (await res.json().catch(() => ({}))) as {
      error?: string;
      code?: RunConflictCode;
      branch?: string;
      commitsAhead?: number;
      base?: string;
    };
    throw runConflictError(body, `phase run failed (${String(res.status)})`);
  }
  return (await res.json()) as { status: string; sessionUuid: string };
}

/**
 * GET /api/epics/{taskId}/phases/{phaseId}/diagnosis — why a phase run did (or
 * did not) achieve anything: the derived outcome, the criteria delta, the
 * deterministic blockers and the executor's last word. READ-ONLY, so it stays
 * available even while a plan run owns the docs.
 */
export function fetchPhaseDiagnosis(taskId: number, phaseId: number): Promise<PhaseDiagnosis> {
  if (MOCK) return mockApi.phaseDiagnosis(taskId, phaseId);
  return get(`/api/epics/${String(taskId)}/phases/${String(phaseId)}/diagnosis`);
}

/**
 * DELETE /api/epics/{taskId}/phases/{phaseId}/branch — reclaim the previous
 * run's branch so the phase can be retried. 200 {deleted, branch}; 409 while a
 * run is active or the branch is checked out.
 */
export async function deletePhaseRunBranch(
  taskId: number,
  phaseId: number,
): Promise<{ deleted: boolean; branch: string }> {
  if (MOCK) return { deleted: true, branch: 'swarm/phase-mock' };
  const res = await fetch(`/api/epics/${String(taskId)}/phases/${String(phaseId)}/branch`, {
    method: 'DELETE',
  });
  if (!res.ok) {
    // Same enriched error the run endpoints throw: a delete refusal is a state
    // the user can resolve (checked out somewhere, is HEAD, a run owns it), and
    // `code` is what lets the caller say which one without matching on prose.
    const body = (await res.json().catch(() => ({}))) as {
      error?: string;
      code?: RunConflictCode;
    };
    throw runConflictError(body, `branch delete failed (${String(res.status)})`);
  }
  return (await res.json()) as { deleted: boolean; branch: string };
}

/**
 * DELETE /api/epics/{taskId}/orphan-branch?branch= — delete a swarm/phase-<id>
 * branch whose id matches no phase row (work stranded under a previous id
 * generation). 200 {deleted, branch}; 409 for a branch outside the
 * swarm/phase-<id> namespace or one that belongs to a live phase row.
 *
 * A SIBLING of deletePhaseRunBranch, not a parameterisation of it: that route
 * derives the branch from the phase id and must stay incapable of naming an
 * arbitrary one. An orphan has no row to derive from, hence the explicit name.
 */
export async function deleteOrphanBranch(
  taskId: number,
  branch: string,
): Promise<{ deleted: boolean; branch: string }> {
  if (MOCK) return { deleted: true, branch };
  const res = await fetch(
    `/api/epics/${String(taskId)}/orphan-branch?branch=${encodeURIComponent(branch)}`,
    { method: 'DELETE' },
  );
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as {
      error?: string;
      code?: RunConflictCode;
    };
    throw runConflictError(body, `orphan branch delete failed (${String(res.status)})`);
  }
  return (await res.json()) as { deleted: boolean; branch: string };
}

/** POST /api/epics/{taskId}/phases/{phaseId}/run/cancel — 202 / 409 when idle. */
export async function cancelEpicPhaseRun(
  taskId: number,
  phaseId: number,
): Promise<{ status: string }> {
  if (MOCK) return { status: 'cancelling' };
  const res = await fetch(`/api/epics/${String(taskId)}/phases/${String(phaseId)}/run/cancel`, {
    method: 'POST',
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `phase run cancel failed (${String(res.status)})`);
  }
  return (await res.json()) as { status: string };
}

/**
 * POST /api/epics/{taskId}/run — hand the WHOLE plan to one agent: a headless
 * session in an isolated worktree that drives core's run-plan skill. 202
 * {status, sessionUuid, agent, mode}; 409 carries the gate reason (already
 * running / a phase run holds the docs / plan not active / already complete) in
 * the error body — surfaced verbatim.
 *
 * The branch-holds-commits 409 is the one rejection that is not just a message:
 * it carries `branch` / `commitsAhead` / `base`, so it throws the same enriched
 * PhaseRunBranchError the phase run throws. Flattening it to `new Error(message)`
 * is what left the operator staring at a raw string with no way to act.
 */
export async function runEpicPlan(
  taskId: number,
  opts: { agent?: string; mode?: PlanRunMode } = {},
): Promise<{ status: string; sessionUuid: string; agent: string; mode: PlanRunMode }> {
  if (MOCK)
    return {
      status: 'running',
      sessionUuid: 'mock-plan-run-uuid',
      agent: opts.agent ?? 'tech-lead',
      mode: opts.mode ?? 'auto',
    };
  const res = await fetch(`/api/epics/${String(taskId)}/run`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ agent: opts.agent ?? '', mode: opts.mode ?? 'auto' }),
  });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as {
      error?: string;
      code?: RunConflictCode;
      branch?: string;
      commitsAhead?: number;
      base?: string;
    };
    throw runConflictError(body, `plan run failed (${String(res.status)})`);
  }
  return (await res.json()) as {
    status: string;
    sessionUuid: string;
    agent: string;
    mode: PlanRunMode;
  };
}

/**
 * DELETE /api/epics/{taskId}/branch — destroy the plan's run branch INCLUDING
 * its commits, the explicit decision behind the branch-dirty 409. 200
 * {deleted, branch} where `deleted` reports whether the branch was actually
 * there (the delete is idempotent, so a no-op must not be claimed as a
 * deletion); 409 while a run owns it, it is checked out, or it is outside the
 * swarm/ namespace the daemon may delete.
 */
export async function deletePlanRunBranch(
  taskId: number,
): Promise<{ deleted: boolean; branch: string }> {
  if (MOCK) return { deleted: true, branch: 'swarm/plan-mock' };
  const res = await fetch(`/api/epics/${String(taskId)}/branch`, { method: 'DELETE' });
  if (!res.ok) {
    // Same enriched error the phase-scoped delete throws — the plan and phase
    // surfaces answer the same condition with the same `code` (runconflict.go),
    // so one client-side shape covers both.
    const body = (await res.json().catch(() => ({}))) as {
      error?: string;
      code?: RunConflictCode;
    };
    throw runConflictError(body, `branch delete failed (${String(res.status)})`);
  }
  return (await res.json()) as { deleted: boolean; branch: string };
}

/** POST /api/epics/{taskId}/run/cancel — 202 / 409 when idle. */
export async function cancelEpicPlanRun(taskId: number): Promise<{ status: string }> {
  if (MOCK) return { status: 'cancelling' };
  const res = await fetch(`/api/epics/${String(taskId)}/run/cancel`, { method: 'POST' });
  if (!res.ok) {
    const body = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `plan run cancel failed (${String(res.status)})`);
  }
  return (await res.json()) as { status: string };
}

/** GET /api/epics/{taskId}/docs?path= — read a plan doc (path-confined). */
export function fetchPlanDoc(taskId: number, path: string): Promise<PlanDoc> {
  if (MOCK) return mockApi.planDoc(taskId, path);
  return get(`/api/epics/${String(taskId)}/docs?path=${encodeURIComponent(path)}`);
}

/** PUT /api/epics/{taskId}/docs?path= {content} — overwrite a plan doc (backup). */
export async function savePlanDoc(taskId: number, path: string, content: string): Promise<PlanDoc> {
  if (MOCK) return { path, content, backup: '.backups/mock/doc.md' };
  const res = await fetch(`/api/epics/${String(taskId)}/docs?path=${encodeURIComponent(path)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `save doc failed: ${String(res.status)}`);
  }
  return (await res.json()) as PlanDoc;
}

/**
 * PATCH /api/epics/{taskId}/docs?path= {line, done} — flip one checkbox by
 * 0-based line index (the exact `- [ ]`↔`- [x]` line).
 */
export async function togglePlanCheckbox(
  taskId: number,
  path: string,
  line: number,
  done: boolean,
): Promise<PlanDoc> {
  if (MOCK) return mockApi.togglePlanCheckbox(taskId, path, line, done);
  const res = await fetch(`/api/epics/${String(taskId)}/docs?path=${encodeURIComponent(path)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ line, done }),
  });
  if (!res.ok) {
    const data = (await res.json().catch(() => ({}))) as { error?: string };
    throw new Error(data.error ?? `toggle checkbox failed: ${String(res.status)}`);
  }
  return (await res.json()) as PlanDoc;
}

// --- verification contour (phase 2) ------------------------------------------

/** Per-agent first-pass success rate from trajectory_scores. */
export interface FirstPassRow {
  agent: string;
  sessions: number;
  firstPass: number;
  rate: number;
  /** Distinct anti-pattern kinds detected for this agent (for Retro chips). */
  kinds: string[];
}

export function fetchFirstPassRates(): Promise<FirstPassRow[]> {
  return get('/api/analytics/first-pass');
}

// --- verification contour (phase 2) — LLM-judge trajectory judgments ----------

/** Advisory LLM-judge verdict for one session × agent × model. Scores 1–5 (higher = better). */
export interface TrajectoryJudgment {
  agent: string;
  model: string;
  judgedAt: string;
  endResult: number;
  instructionCompliance: number;
  pitfalls: number;
  toolCalls: number;
  overall: number;
  review: string;
}

/** GET /api/analytics/trajectory-judgments?session=<id> — verdicts for one session.
 * Returns [] when no judgments have been recorded yet. */
export function fetchTrajectoryJudgments(sessionId: number): Promise<TrajectoryJudgment[]> {
  return get(`/api/analytics/trajectory-judgments?session=${String(sessionId)}`);
}
