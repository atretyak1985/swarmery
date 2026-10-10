// Agent picker: a <select> of the registry agents a board task may dispatch as,
// shared by NewTaskModal (create) and TaskModal (edit). Mirrors PlaybookPicker's
// shape — a fetch hook plus a plain, labelled <select> so the control is
// keyboard-native (WCAG).
//
// The roster comes from GET /api/agents/hub?projectId=…, which narrows to the
// project's effective set but still returns other projects' rows in fleet mode.
// The server's own validation (resolveAgentName in internal/api/tasks_board.go)
// accepts only a global agent or one scoped to THIS project, so the same
// predicate is applied here — otherwise the picker would offer names the POST
// then rejects with "unknown agent". Agents from packs the project does not
// enable (enabledInProject=false) are dropped too. Same-named rows across scopes
// fold to one option (the project-scoped row wins, as it is the definition that
// overrides).
//
// Options are grouped by role into <optgroup>s in a fixed order. The role comes
// from the pack's agents/roles.json (read by the daemon, never decided here).
// "" is not "no agent picked yet": the dispatcher never chooses an agent, so ""
// runs the playbook's stages with no persona — the first option says so.

import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useState } from 'react';
import type { AgentRole, AgentRosterRow } from '../api/types';
import { fetchAgentRoster } from '../api/agentHub';

/** A selectable roster row plus whether it overrides an inherited agent. */
export interface PickerAgent extends AgentRosterRow {
  /** A project-scoped agent that shadows a global or pack agent of the same name. */
  projectOverride: boolean;
}

/** Group order in the picker. */
export const ROLE_ORDER: readonly AgentRole[] = [
  'orchestrate',
  'implement',
  'review',
  'research',
  'ops',
  'domain',
];

/** Label of the "" option. */
/** Source text is English like every other message; the Ukrainian catalog carries «Без агента — лише стейджі playbook». */
export const NO_AGENT_LABEL = msg`No agent — playbook stages only`;

/** Past this many agents the picker grows a filter box. */
export const SEARCH_THRESHOLD = 12;

/** Roster entries selectable for a project, name-sorted and de-duplicated. */
export function useAgentRoster(
  projectId: number | null,
  projectSlug: string | null,
): { agents: PickerAgent[]; loading: boolean } {
  const [agents, setAgents] = useState<PickerAgent[]>([]);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    if (projectId === null) {
      setAgents([]);
      setLoading(false);
      return;
    }
    let disposed = false;
    setLoading(true);
    fetchAgentRoster(String(projectId))
      .then((resp) => {
        if (disposed) return;
        setAgents(selectableAgents(resp.agents, projectSlug));
      })
      .catch(() => {
        if (!disposed) setAgents([]);
      })
      .finally(() => {
        if (!disposed) setLoading(false);
      });
    return () => {
      disposed = true;
    };
  }, [projectId, projectSlug]);
  return { agents, loading };
}

/** "core:tech-lead" → "tech-lead"; a bare name is returned as is. */
function bareName(name: string): string {
  return name.slice(name.lastIndexOf(':') + 1);
}

/**
 * Pure part of the hook: scope + enabled-pack filter, fold-by-name, override
 * flag, sort. Exported for tests.
 */
export function selectableAgents(rows: AgentRosterRow[], projectSlug: string | null): PickerAgent[] {
  const eligible = rows.filter(
    (a) => a.enabledInProject && (a.scope === 'global' || a.projectSlug === projectSlug),
  );
  // Bare names of every inherited (global) agent: a project agent named after
  // one of them — "tech-lead" next to "core:tech-lead", or a same-named
  // user-level agent — is an override.
  const inherited = new Set(eligible.filter((a) => a.scope === 'global').map((a) => bareName(a.name)));
  const byName = new Map<string, PickerAgent>();
  for (const a of eligible) {
    const seen = byName.get(a.name);
    // A project-scoped definition overrides a global one of the same name.
    if (seen === undefined || (seen.scope === 'global' && a.scope === 'project')) {
      byName.set(a.name, { ...a, projectOverride: a.scope === 'project' && inherited.has(bareName(a.name)) });
    }
  }
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/** Agents in `ROLE_ORDER` groups; empty groups are dropped. Exported for tests. */
export function groupByRole(agents: PickerAgent[]): { role: AgentRole; agents: PickerAgent[] }[] {
  return ROLE_ORDER.map((role) => ({ role, agents: agents.filter((a) => a.role === role) })).filter(
    (g) => g.agents.length > 0,
  );
}

function matches(a: PickerAgent, q: string): boolean {
  return a.name.toLowerCase().includes(q) || (a.description ?? '').toLowerCase().includes(q);
}

/**
 * A <select> bound to an agent name. `value` is the selected name ("" = no
 * agent: the playbook's stages run without a persona); `onChange` receives the
 * new name. Renders an unknown stored value as its own option so an agent
 * whose file has since gone does not silently reset the field. Past
 * SEARCH_THRESHOLD agents a filter box narrows the options; the selected one
 * always stays listed.
 */
export function AgentSelect({
  agents,
  value,
  onChange,
  disabled = false,
  id,
}: {
  agents: PickerAgent[];
  value: string;
  onChange: (name: string) => void;
  disabled?: boolean;
  id?: string;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const [query, setQuery] = useState('');
  const known = agents.some((a) => a.name === value);
  const searchable = agents.length > SEARCH_THRESHOLD;
  const q = searchable ? query.trim().toLowerCase() : '';
  const groups = useMemo(
    () => groupByRole(q === '' ? agents : agents.filter((a) => a.name === value || matches(a, q))),
    [agents, q, value],
  );
  const fieldClass =
    'w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink outline-none transition-colors hover:border-line-strong focus:border-ink-dim disabled:opacity-50';
  return (
    <div className="flex flex-col gap-1">
      {searchable && (
        <input
          type="search"
          value={query}
          disabled={disabled}
          aria-label={t`filter agents`}
          placeholder={t`filter agents…`}
          onChange={(e) => setQuery(e.target.value)}
          className={fieldClass}
        />
      )}
      <select
        id={id}
        value={value}
        disabled={disabled}
        aria-label={t`agent`}
        onChange={(e) => onChange(e.target.value)}
        className={fieldClass}
      >
        <option value="">{i18n._(NO_AGENT_LABEL)}</option>
        {value !== '' && !known && <option value={value}>{t`@${value} (unknown)`}</option>}
        {groups.map((g) => (
          <optgroup key={g.role} label={g.role}>
            {g.agents.map((a) => (
              <option key={`${a.name}:${String(a.id)}`} value={a.name}>
                @{a.name}
                {a.projectOverride ? t` (project override)` : ''}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
    </div>
  );
}

/**
 * The selected agent's one-line description, with a `project override` badge
 * when the project's own .claude/agents/ definition shadows an inherited one.
 * Null when nothing (or an unknown name) is selected.
 */
export function AgentHint({
  agents,
  value,
}: {
  agents: PickerAgent[];
  value: string;
}): JSX.Element | null {
  const { t } = useLingui();
  if (value === '') return null;
  const agent = agents.find((a) => a.name === value);
  if (agent === undefined) return null;
  if (!agent.projectOverride && agent.description === null) return null;
  return (
    <div className="mt-1 flex items-start gap-1.5">
      {agent.projectOverride && (
        <span
          className="shrink-0 rounded-full border border-line px-1.5 py-[1px] font-mono text-[9px] text-ink-dim uppercase"
          data-tip={t`this project's .claude/agents/ definition replaces the inherited one`}
        >
          <Trans>project override</Trans>
        </span>
      )}
      {agent.description !== null && (
        <span className="font-mono text-[10px] leading-snug text-ink-faint">{agent.description}</span>
      )}
    </div>
  );
}
