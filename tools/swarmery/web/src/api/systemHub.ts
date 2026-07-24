// Typed client for the fusion-phase-18 /api/system/* Hub endpoints: the catalog
// grouped by ROLE (Toolkit = Skills/Commands/Templates · Hooks · Insights).
// Contracts in ./types.ts, Go DTOs in internal/api/system_hub.go. Its own module
// so the System Hub UI wave touches no shared client file: MOCK dispatch mirrors
// ../api.ts, fixtures come from ../mock/systemHub.ts.
//
// Every endpoint here is READ-ONLY except copyTemplate — the ONE new write path
// (copy a built-in into the project so it can be customised, mirroring the
// playbook duplicate-to-project idiom). Skill/hook definition EDITING is NOT
// here: it reuses the existing System write surface (../api/system.ts).

import type {
  CommandHub,
  HookHub,
  SkillHub,
  SystemHubSummary,
  SystemTemplate,
  SystemTemplateContent,
  SystemTemplateCopyResponse,
} from './types';
import { MOCK } from '../api';
import { mockSystemHubApi } from '../mock/systemHub';

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path);
  if (!res.ok) {
    throw new Error(`GET ${path}: ${String(res.status)}`);
  }
  return (await res.json()) as T;
}

/** ?projectId= suffix (slug or id) — omitted when unscoped (fleet mode). */
function scopeQ(projectId?: string): string {
  return projectId !== undefined && projectId !== ''
    ? `?projectId=${encodeURIComponent(projectId)}`
    : '';
}

/** GET /api/system/hub/summary?projectId= — the nav count badges. */
export function fetchSystemHubSummary(projectId?: string): Promise<SystemHubSummary> {
  if (MOCK) return mockSystemHubApi.summary();
  return get(`/api/system/hub/summary${scopeQ(projectId)}`);
}

/** GET /api/system/skills/{id}/hub?projectId= — a skill's profile bundle. */
export function fetchSkillHub(id: number, projectId?: string): Promise<SkillHub> {
  if (MOCK) return mockSystemHubApi.skill(id);
  return get(`/api/system/skills/${String(id)}/hub${scopeQ(projectId)}`);
}

/** GET /api/system/hooks/{id}/hub — a hook's profile (config + lint). */
export function fetchHookHub(id: number): Promise<HookHub> {
  if (MOCK) return mockSystemHubApi.hook(id);
  return get(`/api/system/hooks/${String(id)}/hub`);
}

/** GET /api/system/commands/{id}/hub?projectId= — a command's profile. */
export function fetchCommandHub(id: number, projectId?: string): Promise<CommandHub> {
  if (MOCK) return mockSystemHubApi.command(id);
  return get(`/api/system/commands/${String(id)}/hub${scopeQ(projectId)}`);
}

/** GET /api/system/templates?projectId= — the effective template list. */
export function fetchSystemTemplates(projectId?: string): Promise<SystemTemplate[]> {
  if (MOCK) return mockSystemHubApi.templates(projectId);
  return get(`/api/system/templates${scopeQ(projectId)}`);
}

/** GET /api/system/templates/{name}?projectId= — one template's content (RO). */
export function fetchSystemTemplate(
  name: string,
  projectId?: string,
): Promise<SystemTemplateContent> {
  if (MOCK) return mockSystemHubApi.template(name, projectId);
  return get(`/api/system/templates/${encodeURIComponent(name)}${scopeQ(projectId)}`);
}

/**
 * Typed failure of the template copy write. Carries the parsed status so the UI
 * can branch on the contract: 409 (already customised / O_EXCL), 403 (readonly),
 * 404 (unknown project/template), 400 (bad name).
 */
export class TemplateCopyError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'TemplateCopyError';
    this.status = status;
  }
  /** True when the project already has this template (409 O_EXCL). */
  get alreadyExists(): boolean {
    return this.status === 409;
  }
  get readonly(): boolean {
    return this.status === 403;
  }
}

/**
 * POST /api/system/templates/{name}/copy?projectId= — copy a built-in into the
 * project's .claude/templates/. There is deliberately NO overwrite mode: a 409
 * means the project already customised it (the O_EXCL contract).
 */
export async function copyTemplateToProject(
  name: string,
  projectId: string,
): Promise<SystemTemplateCopyResponse> {
  if (MOCK) return mockSystemHubApi.copyTemplate(name, projectId);
  const res = await fetch(
    `/api/system/templates/${encodeURIComponent(name)}/copy?projectId=${encodeURIComponent(projectId)}`,
    { method: 'POST', headers: { 'Content-Type': 'application/json' } },
  );
  if (res.ok) return (await res.json()) as SystemTemplateCopyResponse;
  let msg = `copy failed: ${String(res.status)}`;
  try {
    const body = (await res.json()) as { error?: unknown };
    if (typeof body.error === 'string') msg = body.error;
  } catch {
    // non-JSON error body — status message stands
  }
  throw new TemplateCopyError(res.status, msg);
}
