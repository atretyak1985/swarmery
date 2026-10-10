// System Hub profile panels (fusion phase 18): the per-item detail bodies for
// each Toolkit/Hooks category. Pure presentational + their own data fetch —
// they take a selected id and render the profile bundle with the existing design
// language (stat tiles, mono rows, sparkline). The Skill Definition tab embeds
// the existing versioned System editor (SystemItemPanel) — NOT reimplemented.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import type {
  CommandHub,
  HookHub,
  SkillHub,
  SystemTemplateContent,
} from '../../api/types';
import {
  copyTemplateToProject,
  fetchCommandHub,
  fetchHookHub,
  fetchSkillHub,
  fetchSystemTemplate,
  TemplateCopyError,
} from '../../api/systemHub';
import { fmtAgo } from '../../lib/format';
import { Empty, ErrorBox, Loading } from '../../components/ui';
import { Sparkline } from '../../components/Sparkline';
import { SystemItemPanel } from '../system/ItemDetail';
import { DocsPanel } from '../system/DocsPanel';
import { guidePath } from '../system/docsSection';

/* ----- shared atoms ----- */

function StatTile({ label, value, sub }: { label: string; value: string; sub?: string }): JSX.Element {
  return (
    <div className="rounded-xl border border-line bg-bg px-3.5 py-3">
      <div className="font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">{label}</div>
      <div className="mt-1 font-display text-[22px] leading-none font-medium text-ink">{value}</div>
      {sub !== undefined && <div className="mt-1 font-mono text-[10.5px] text-ink-dim">{sub}</div>}
    </div>
  );
}

/** A monospace content viewer (redacted markdown) — read-only. */
function ContentBlock({ content }: { content: string }): JSX.Element {
  if (content === '')
    return (
      <Empty>
        <Trans>no content available (the file is empty or unreadable)</Trans>
      </Empty>
    );
  return (
    <pre className="overflow-x-auto rounded-xl border border-line bg-bg px-3.5 py-3 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
      {content}
    </pre>
  );
}

/* ================= Skill ================= */

type SkillTab = 'overview' | 'docs' | 'usage' | 'definition';

export function SkillProfile({
  id,
  tab,
  projectId,
  projectNames,
  defRefresh,
  onDefinitionMutated,
}: {
  id: number;
  tab: SkillTab;
  projectId: string | null;
  projectNames: Record<string, string>;
  defRefresh: number;
  onDefinitionMutated: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [hub, setHub] = useState<SkillHub | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback((): void => {
    setError(null);
    fetchSkillHub(id, projectId ?? undefined)
      .then(setHub)
      .catch((e: unknown) => setError(String(e)));
  }, [id, projectId]);
  useEffect(load, [load]);

  // Docs tab: the skill's `# How to use` guide. Same panel, same registry id,
  // 'docs' variant — mirrors AgentHub.tsx's docs branch. The hub's own tab bar
  // is the section switcher here, so the panel renders the guide alone.
  if (tab === 'docs') {
    return (
      <SystemItemPanel
        kind="skills"
        id={id}
        refreshKey={defRefresh}
        projectNames={projectNames}
        onClose={() => undefined}
        onMutated={onDefinitionMutated}
        onDeleted={onDefinitionMutated}
        onReadonly={() => undefined}
        variant="docs"
      />
    );
  }

  // Definition tab: reuse the existing versioned System editor verbatim (skills
  // kind). It fetches its own detail by the same registry id, so it works
  // standalone — create/delete stay on the System page.
  if (tab === 'definition') {
    return (
      <SystemItemPanel
        kind="skills"
        id={id}
        refreshKey={defRefresh}
        projectNames={projectNames}
        onClose={() => undefined}
        onMutated={onDefinitionMutated}
        onDeleted={onDefinitionMutated}
        onReadonly={() => undefined}
      />
    );
  }

  if (error !== null) return <ErrorBox message={error} onRetry={load} />;
  if (hub === null) return <Loading label={t`skill…`} />;

  if (tab === 'usage') {
    if (hub.sessions.length === 0)
      return (
        <Empty>
          <Trans>no invoking sessions in the last 30 days</Trans>
        </Empty>
      );
    return (
      <div className="overflow-hidden rounded-xl border border-line">
        {hub.sessions.map((s, i) => (
          <Link
            key={`${s.sessionUuid}-${String(i)}`}
            to={`/sessions/${encodeURIComponent(s.sessionUuid)}`}
            className="flex items-center gap-3 border-b border-line-soft px-3.5 py-2.5 transition-colors last:border-b-0 hover:bg-surface"
          >
            <span className={`font-mono text-[11px] ${s.status === 'error' ? 'text-red' : 'text-green'}`}>
              {s.status === 'error' ? '▲' : '●'}
            </span>
            <div className="min-w-0 flex-1">
              <div className="truncate text-[12.5px] text-ink">{s.sessionTitle || s.sessionUuid}</div>
              <div className="font-mono text-[10px] text-ink-faint">
                {s.projectSlug !== '' ? `${s.projectSlug} · ` : ''}
                {fmtAgo(s.ts)}
              </div>
            </div>
          </Link>
        ))}
      </div>
    );
  }

  // Overview
  const spark = hub.usage.byDay.map((d) => d.count);
  const lastUsed = hub.usage.lastUsed !== null ? fmtAgo(hub.usage.lastUsed) : null;
  const tone = hub.usage.errors > 0 && hub.usage.invocations > 0 && hub.usage.errors / hub.usage.invocations >= 0.3 ? 'amber' : 'dim';
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
        <StatTile label={t`invocations 30d`} value={String(hub.usage.invocations)} />
        <StatTile label={t`sessions`} value={String(hub.usage.sessions)} />
        <StatTile label={t`projects`} value={String(hub.usage.projects)} />
        <StatTile label={t`errors`} value={String(hub.usage.errors)} />
      </div>
      <div className="rounded-xl border border-line bg-bg px-3.5 py-3">
        <div className="flex items-baseline justify-between">
          <span className="font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
            <Trans>invocations / day (30d)</Trans>
          </span>
          <span className="font-mono text-[10.5px] text-ink-dim">
            {lastUsed !== null ? t`last used ${lastUsed}` : t`never used`}
          </span>
        </div>
        {spark.length >= 2 ? (
          <Sparkline values={spark} highlight={spark.length - 1} tone={tone} />
        ) : (
          <div className="mt-2 font-mono text-[11px] text-ink-faint">
            <Trans>not enough data</Trans>
          </div>
        )}
        {hub.usage.approximate && (
          <div className="mt-2 font-mono text-[10.5px] text-amber">
            ▲ <Trans>approximate — the window overlaps rolled-up days, so counts may undercount</Trans>
          </div>
        )}
      </div>
      <div className="font-mono text-[10.5px] text-ink-faint">
        <Trans>numbers match the Analytics skills panel for the same 30-day window</Trans>
      </div>
    </div>
  );
}

/* ================= Hook ================= */

export function HookProfile({ id }: { id: number }): JSX.Element {
  const { t } = useLingui();
  const [hub, setHub] = useState<HookHub | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback((): void => {
    setError(null);
    fetchHookHub(id)
      .then(setHub)
      .catch((e: unknown) => setError(String(e)));
  }, [id]);
  useEffect(load, [load]);

  if (error !== null) return <ErrorBox message={error} onRetry={load} />;
  if (hub === null) return <Loading label={t`hook…`} />;
  const managed = hub.managed;

  return (
    <div className="space-y-4">
      <div className="rounded-xl border border-line bg-bg px-3.5 py-3">
        <div className="grid grid-cols-2 gap-x-4 gap-y-2 font-mono text-[11.5px]">
          <div>
            <span className="text-ink-faint">
              <Trans>event</Trans>
            </span>{' '}
            <span className="text-ink">{hub.event}</span>
          </div>
          <div>
            <span className="text-ink-faint">
              <Trans>matcher</Trans>
            </span>{' '}
            <span className="text-ink">{hub.matcher ?? '*'}</span>
          </div>
          <div>
            <span className="text-ink-faint">
              <Trans>timeout</Trans>
            </span>{' '}
            <span className={hub.timeout === null ? 'text-amber' : 'text-ink'}>
              {hub.timeout === null ? t`none` : `${String(hub.timeout)}s`}
            </span>
          </div>
          <div>
            <span className="text-ink-faint">
              <Trans>scope</Trans>
            </span>{' '}
            <span className="text-ink">{hub.scope}</span>
          </div>
        </div>
        <div className="mt-2 overflow-x-auto rounded-md bg-surface px-2.5 py-1.5 font-mono text-[11px] whitespace-pre text-ink-2">
          {hub.command}
        </div>
        <div className="mt-1 font-mono text-[10.5px] break-all text-ink-faint">{hub.sourceFile}</div>
        {hub.managed !== null && (
          <div className="mt-1.5 inline-block rounded-full border border-brand/40 px-2 py-px font-mono text-[10px] text-brand">
            <Trans>managed · {managed}</Trans>
          </div>
        )}
      </div>

      {hub.lint.length > 0 && (
        <div>
          <div className="mb-1.5 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
            <Trans>lint findings</Trans>
          </div>
          <div className="space-y-1.5">
            {hub.lint.map((f, i) => (
              <div
                key={`${f.rule}-${String(i)}`}
                className={`rounded-lg border px-3 py-2 text-[12px] ${
                  f.severity === 'error' ? 'border-red/40 text-red' : f.severity === 'warn' ? 'border-amber/40 text-amber' : 'border-line text-ink-dim'
                }`}
              >
                <span className="mr-1.5 font-mono text-[10px]">{f.rule}</span>
                {f.message}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Honest note: firing telemetry is NOT tracked (no fake zeros). */}
      {!hub.firingTelemetry && (
        <div className="rounded-lg border border-line bg-bg px-3 py-2 font-mono text-[10.5px] text-ink-dim">
          <Trans>
            hook firing telemetry is not tracked yet — this profile shows configuration and lint only,
            not how often the hook fired (a documented follow-up)
          </Trans>
        </div>
      )}

      <div className="font-mono text-[10.5px] text-ink-faint">
        <Trans>
          edit / enable / disable this hook from the System page → Hooks tab (the existing write
          surface)
        </Trans>
      </div>
    </div>
  );
}

/* ================= Command ================= */

type CommandTab = 'overview' | 'docs' | 'content';

export function CommandProfile({ id, tab, projectId }: { id: number; tab: CommandTab; projectId: string | null }): JSX.Element {
  const { t } = useLingui();
  const [hub, setHub] = useState<CommandHub | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback((): void => {
    setError(null);
    fetchCommandHub(id, projectId ?? undefined)
      .then(setHub)
      .catch((e: unknown) => setError(String(e)));
  }, [id, projectId]);
  useEffect(load, [load]);

  if (error !== null) return <ErrorBox message={error} onRetry={load} />;
  if (hub === null) return <Loading label={t`command…`} />;

  // Docs tab: the command's `# How to use` guide — commands have no editor, so
  // this reads straight from the hub bundle (unlike skills/agents, no
  // SystemItemPanel indirection is needed). Name matches CommandDetail.tsx's
  // `/${cmd.name}` display convention.
  if (tab === 'docs') {
    return <DocsPanel docs={hub.docs} path={guidePath(hub.path, 'commands')} name={`/${hub.name}`} />;
  }

  if (tab === 'content') return <ContentBlock content={hub.content} />;

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3">
        <StatTile
          label={t`invocations 30d`}
          value={String(hub.usage.invocations)}
          sub={t`approximate`}
        />
        <StatTile label={t`scope`} value={hub.scope} />
        <StatTile label={t`origin`} value={hub.pluginName ?? hub.origin} />
      </div>
      {hub.description !== null && (
        <div className="rounded-xl border border-line bg-bg px-3.5 py-3 text-[12.5px] text-ink-dim">
          {hub.description}
        </div>
      )}
      <div className="rounded-lg border border-line bg-bg px-3 py-2 font-mono text-[10.5px] text-amber">
        ▲{' '}
        <Trans>
          usage is approximate — slash-command invocations are inferred from prompt text, not an
          authoritative event
        </Trans>
      </div>
      <div className="font-mono text-[10.5px] break-all text-ink-faint">{hub.path}</div>
    </div>
  );
}

/* ================= Template ================= */

export function TemplateProfile({
  name,
  projectId,
  onCopied,
}: {
  name: string;
  projectId: string | null;
  /** After a successful copy-to-project, refresh the roster (badge flips). */
  onCopied: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [tmpl, setTmpl] = useState<SystemTemplateContent | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [copyState, setCopyState] = useState<{ busy: boolean; msg: string | null; ok: boolean }>({
    busy: false,
    msg: null,
    ok: false,
  });

  const load = useCallback((): void => {
    setError(null);
    fetchSystemTemplate(name, projectId ?? undefined)
      .then(setTmpl)
      .catch((e: unknown) => setError(String(e)));
  }, [name, projectId]);
  useEffect(load, [load]);

  const doCopy = (): void => {
    if (projectId === null) return;
    setCopyState({ busy: true, msg: null, ok: false });
    copyTemplateToProject(name, projectId)
      .then((res) => {
        const path = res.path;
        setCopyState({ busy: false, msg: t`copied → ${path}`, ok: true });
        onCopied();
        load();
      })
      .catch((e: unknown) => {
        const msg =
          e instanceof TemplateCopyError
            ? e.alreadyExists
              ? t`this project already has its own copy of this template`
              : e.readonly
                ? t`system is in readonly mode — copy is disabled`
                : e.message
            : String(e);
        setCopyState({ busy: false, msg, ok: false });
      });
  };

  if (error !== null) return <ErrorBox message={error} onRetry={load} />;
  if (tmpl === null) return <Loading label={t`template…`} />;

  // Copy is offered only in project mode, and only for a built-in (a project
  // override already resolves locally — nothing to copy).
  const canCopy = projectId !== null && tmpl.source === 'plugin';

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <span
          className={`rounded-full border px-2 py-px font-mono text-[10px] ${
            /* i18n-ignore — server resolution value, compared */ tmpl.resolution === 'project override'
              ? 'border-brand/40 text-brand'
              : 'border-line-strong text-ink-dim'
          }`}
        >
          {tmpl.resolution}
        </span>
        <span className="font-mono text-[10.5px] break-all text-ink-faint">{tmpl.path}</span>
        {canCopy && (
          <button
            type="button"
            onClick={doCopy}
            disabled={copyState.busy}
            className="ml-auto rounded-lg border border-line-strong bg-field px-3 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-40"
          >
            {copyState.busy ? t`copying…` : t`Copy to project`}
          </button>
        )}
      </div>
      {copyState.msg !== null && (
        <div
          className={`rounded-lg border px-3 py-2 font-mono text-[11px] ${
            copyState.ok ? 'border-green/40 text-green' : 'border-amber/45 text-amber'
          }`}
          role="status"
        >
          {copyState.msg}
        </div>
      )}
      {projectId !== null && tmpl.source === 'plugin' && (
        <div className="font-mono text-[10.5px] text-ink-faint">
          <Trans>
            copying writes an editable copy into this project's .claude/templates/ — project templates
            override built-ins (the graduation rule)
          </Trans>
        </div>
      )}
      <ContentBlock content={tmpl.content} />
    </div>
  );
}
