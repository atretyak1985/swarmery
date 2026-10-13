// Mods tab (System screen, mods-swarmery-link phase 9): every Claude Code mod
// — a plugin whose hooks/hooks.json lists `modules` — that internal/sysscan
// found in the plugin cache or in CLAUDE_CODE_PLUGIN_DIRS, with the risk
// badges internal/modscan derives from what `claude plugin validate --strict
// --json` says each module hooks and calls.
//
// Read-only audit: the folder path is shown (and copyable), the module source
// never is — the detail carries the validator's report, not the files. The one
// action is Rescan (POST, local origin only), which re-validates every mod and
// then refetches the list. The daemon's WS layer does not forward mod
// `system_item_updated` notes yet, so the tab refetches on mount and after a
// rescan rather than live.

import type { MessageDescriptor } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import type { ModBadge, ModDetail, ModGatingHook, ModRescanResult, ModSummary } from '../../api/types';
import { fetchSystemMod, fetchSystemMods, rescanSystemMods } from '../../api/system';
import { CopyIdBadge } from '../../components/CopyIdBadge';
import { Empty, ErrorBox, Loading, SectionTitle } from '../../components/ui';
import { fmtDateTime } from '../../lib/format';

/** Badge labels by rule — the server sends English; the UI localises by rule
 * and falls back to the server label for a rule it does not know yet. */
const RULE_LABELS: Record<string, MessageDescriptor> = {
  'tool.check': msg`can approve tool calls`,
  'prompt.submit': msg`drives the session`,
  'turn.abort': msg`drives the session`,
  'http.fetch': msg`network`,
  'process.*': msg`spawns processes`,
  'fs.write': msg`writes files`,
  'session.append': msg`rewrites what Claude reads`,
  'fs.read': msg`reads files`,
  'store.*': msg`keeps a store`,
  'model.*': msg`calls the model`,
  validate: msg`not validated`,
};

const LEVEL_TONES: Record<ModBadge['level'], string> = {
  red: 'border-red/45 bg-red/5 text-red',
  yellow: 'border-amber/45 bg-amber/5 text-amber',
  unknown: 'border-line-strong text-ink-dim',
};

const LEVEL_NAMES: Record<ModBadge['level'], MessageDescriptor> = {
  red: msg`high risk`,
  yellow: msg`moderate risk`,
  unknown: msg`unknown risk`,
};

const LEVEL_GLYPHS: Record<ModBadge['level'], string> = { red: '▲', yellow: '●', unknown: '?' };

export function RiskBadge({ badge }: { badge: ModBadge }): JSX.Element {
  const { t, i18n } = useLingui();
  const descriptor = RULE_LABELS[badge.rule];
  const label = descriptor !== undefined ? i18n._(descriptor) : badge.label;
  const level = i18n._(LEVEL_NAMES[badge.level]);
  const matches = badge.matches.join(', ');
  const tip = matches === '' ? t`${level}: ${badge.rule}` : t`${level}: ${badge.rule} — ${matches}`;
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full border px-2 py-px font-mono text-[10px] whitespace-nowrap ${LEVEL_TONES[badge.level]}`}
      data-tip={tip}
      data-level={badge.level}
    >
      <span aria-hidden="true">{LEVEL_GLYPHS[badge.level]}</span>
      <span className="sr-only">{level}: </span>
      {label}
    </span>
  );
}

function RiskBadges({ risk }: { risk: ModBadge[] }): JSX.Element {
  const { t } = useLingui();
  if (risk.length === 0) {
    return (
      <span className="font-mono text-[10.5px] text-green" data-tip={t`validated: no risky event or call`}>
        <Trans>no risk badges</Trans>
      </span>
    );
  }
  return (
    <span className="flex flex-wrap gap-1">
      {risk.map((b) => (
        <RiskBadge key={b.rule} badge={b} />
      ))}
    </span>
  );
}

/** A short mono list, or a dash when empty. */
function MonoList({ items }: { items: string[] }): JSX.Element {
  if (items.length === 0) return <span className="text-ink-faint">—</span>;
  return <span className="break-words">{items.join(', ')}</span>;
}

function SourceLabel({ source }: { source: ModSummary['source'] }): JSX.Element {
  return source === 'cache' ? <Trans>plugin cache</Trans> : <Trans>plugin dir</Trans>;
}

/* ----- list ----- */

function ModRow({
  mod,
  selected,
  onSelect,
}: {
  mod: ModSummary;
  selected: boolean;
  onSelect: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const name = mod.name;
  return (
    <tr className={`border-b border-line-soft align-top last:border-b-0 ${selected ? 'bg-surface2' : ''}`}>
      <td className="px-3 py-2">
        <button
          type="button"
          onClick={onSelect}
          aria-expanded={selected}
          aria-controls="mod-detail"
          aria-label={selected ? t`hide the validate report of ${name}` : t`show the validate report of ${name}`}
          className="text-left text-[13px] font-semibold text-ink hover:text-brand focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
        >
          {mod.name}
        </button>
        {mod.pluginId !== null && (
          <div className="font-mono text-[10.5px] whitespace-nowrap text-ink-dim">{mod.pluginId}</div>
        )}
        {mod.errors.length > 0 && (
          <div className="font-mono text-[10.5px] whitespace-nowrap text-red">
            {plural(mod.errors.length, {
              one: '# validate error',
              few: '# validate errors',
              many: '# validate errors',
              other: '# validate errors',
            })}
          </div>
        )}
      </td>
      <td className="px-3 py-2 font-mono text-[11px] whitespace-nowrap text-ink-2">
        <SourceLabel source={mod.source} />
      </td>
      <td className="px-3 py-2 font-mono text-[11px] whitespace-nowrap text-ink-2">{mod.version ?? '—'}</td>
      <td className="px-3 py-2 font-mono text-[11px] text-ink-2">
        <div className="text-ink">
          {plural(mod.events.length, { one: '# event', few: '# events', many: '# events', other: '# events' })}
        </div>
        <div className="text-[10.5px] text-ink-dim">
          <MonoList items={mod.events} />
        </div>
      </td>
      <td className="px-3 py-2 font-mono text-[11px] text-ink-2">
        <div className="text-ink">
          {plural(mod.uses.length, { one: '# call', few: '# calls', many: '# calls', other: '# calls' })}
        </div>
        <div className="text-[10.5px] text-ink-dim">
          <MonoList items={mod.uses} />
        </div>
      </td>
      <td className="px-3 py-2">
        <RiskBadges risk={mod.risk} />
      </td>
    </tr>
  );
}

/* ----- detail ----- */

function Field({ label, children }: { label: string; children: ReactNode }): JSX.Element {
  return (
    <div className="grid grid-cols-[110px_minmax(0,1fr)] gap-2 border-b border-line-soft py-1.5 last:border-b-0">
      <dt className="font-mono text-[10px] tracking-[0.06em] text-ink-faint uppercase">{label}</dt>
      <dd className="min-w-0 font-mono text-[11.5px] text-ink-2">{children}</dd>
    </div>
  );
}

function MessageList({ items, tone }: { items: string[]; tone: string }): JSX.Element {
  if (items.length === 0) return <span className="text-ink-faint">—</span>;
  return (
    <ul className={`list-disc space-y-0.5 pl-4 break-words ${tone}`}>
      {items.map((m, i) => (
        // Index in the key: validator messages may repeat verbatim.
        <li key={`${String(i)}:${m}`}>{m}</li>
      ))}
    </ul>
  );
}

function GatingHooks({ hooks }: { hooks: ModGatingHook[] }): JSX.Element {
  const { t } = useLingui();
  if (hooks.length === 0) return <span className="text-ink-faint">—</span>;
  return (
    <table className="w-full border-collapse text-left">
      <thead>
        <tr className="text-[10px] text-ink-faint">
          <th className="pr-2 font-normal">{t`module`}</th>
          <th className="pr-2 font-normal">{t`pattern`}</th>
          <th className="pr-2 font-normal">{t`hook`}</th>
          <th className="font-normal">{t`.catch`}</th>
        </tr>
      </thead>
      <tbody>
        {hooks.map((h) => (
          <tr key={`${h.module}|${h.pattern}|${h.hook}`}>
            <td className="pr-2 break-all">{h.module}</td>
            <td className="pr-2 break-all">{h.pattern}</td>
            <td className="pr-2 break-all">{h.hook}</td>
            <td className={h.hasCatch ? 'text-green' : 'text-amber'}>{h.hasCatch ? t`yes` : t`no`}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function ModDetailPanel({ id, onClose }: { id: number; onClose: () => void }): JSX.Element {
  const { t } = useLingui();
  const [detail, setDetail] = useState<ModDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setDetail(null);
    setError(null);
    fetchSystemMod(id)
      .then((d) => {
        if (!cancelled) setDetail(d);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [id, attempt]);

  // Escape closes the panel, like the other System detail rails.
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [onClose]);

  const name = detail?.name ?? '';
  return (
    <section
      id="mod-detail"
      aria-label={detail === null ? t`mod validate report` : t`validate report of ${name}`}
      className="mt-3 rounded-xl border border-line bg-surface px-3.5 py-3"
    >
      <div className="flex items-start gap-2">
        <SectionTitle flush>
          {detail === null ? <Trans>Validate report</Trans> : <Trans>Validate report · {name}</Trans>}
        </SectionTitle>
        <button
          type="button"
          onClick={onClose}
          className="ml-auto rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[11px] text-ink-2 transition-colors hover:bg-surface2"
        >
          <Trans>close</Trans>
        </button>
      </div>
      {error !== null && <ErrorBox message={error} onRetry={() => setAttempt((a) => a + 1)} />}
      {error === null && detail === null && <Loading label={t`validate report…`} />}
      {detail !== null && (
        <>
          <div className="mt-1 mb-3">
            <RiskBadges risk={detail.risk} />
          </div>
          <dl>
            <Field label={t`folder`}>
              <CopyIdBadge id={detail.dir} label={t`folder path`} />
            </Field>
            <Field label={t`source`}>
              <SourceLabel source={detail.source} />
              {detail.pluginId !== null ? ` · ${detail.pluginId}` : ''}
            </Field>
            <Field label={t`version`}>{detail.version ?? '—'}</Field>
            <Field label={t`validated`}>
              {detail.validatedAt === null ? '—' : fmtDateTime(detail.validatedAt)}
              {' · '}
              {!detail.report.validated ? (
                <span className="text-ink-dim">{t`no report`}</span>
              ) : detail.report.success ? (
                <span className="text-green">{t`passed`}</span>
              ) : (
                <span className="text-red">{t`failed`}</span>
              )}
            </Field>
            <Field label={t`module sha`}>
              {detail.moduleSha === null ? (
                <span className="text-ink-dim">{t`not hashed — re-validated on the next scan`}</span>
              ) : (
                <span data-tip-mono data-tip={detail.moduleSha}>
                  {detail.moduleSha.slice(0, 12)}
                </span>
              )}
            </Field>
            <Field label={t`errors`}>
              <MessageList items={detail.report.errors.length > 0 ? detail.report.errors : detail.errors} tone="text-red" />
            </Field>
            <Field label={t`warnings`}>
              <MessageList items={detail.report.warnings} tone="text-amber" />
            </Field>
            <Field label={t`events`}>
              <MonoList items={detail.report.events} />
            </Field>
            <Field label={t`calls`}>
              <MonoList items={detail.report.uses} />
            </Field>
            <Field label={t`env reads`}>
              <MonoList items={detail.report.env.reads} />
            </Field>
            <Field label={t`env writes`}>
              <MonoList items={detail.report.env.writes} />
            </Field>
            <Field label={t`state`}>
              <MonoList items={detail.report.state} />
            </Field>
            <Field label={t`gating hooks`}>
              <GatingHooks hooks={detail.report.gatingHooks} />
            </Field>
          </dl>
        </>
      )}
    </section>
  );
}

/* ----- tab ----- */

export function ModsTab(): JSX.Element {
  const { t } = useLingui();
  const [mods, setMods] = useState<ModSummary[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [selected, setSelected] = useState<number | null>(null);
  const [rescanning, setRescanning] = useState(false);
  const [rescanError, setRescanError] = useState<string | null>(null);
  const [lastRescan, setLastRescan] = useState<ModRescanResult | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetchSystemMods()
      .then((list) => {
        if (cancelled) return;
        setMods(list);
        setError(null);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [attempt]);

  // A selected mod that a rescan swept away closes its panel.
  useEffect(() => {
    if (selected !== null && mods !== null && !mods.some((m) => m.id === selected)) setSelected(null);
  }, [mods, selected]);

  const refetch = (): void => setAttempt((a) => a + 1);

  const rescan = (): void => {
    setRescanning(true);
    setRescanError(null);
    rescanSystemMods()
      .then((res) => {
        setLastRescan(res);
        refetch();
      })
      .catch((e: unknown) => setRescanError(e instanceof Error ? e.message : String(e)))
      .finally(() => setRescanning(false));
  };

  const closeDetail = useCallback((): void => setSelected(null), []);

  return (
    <div className="flex h-full flex-col pb-4">
      <div className="flex shrink-0 flex-wrap items-center gap-2 pt-1 pb-3">
        <p className="min-w-0 flex-1 text-[12.5px] text-ink-dim">
          <Trans>
            Plugins whose hooks.json lists modules, from the plugin cache and CLAUDE_CODE_PLUGIN_DIRS. Badges come
            from what <span className="font-mono">claude plugin validate</span> reports each module hooks and calls.
          </Trans>
        </p>
        <button
          type="button"
          onClick={rescan}
          disabled={rescanning}
          aria-busy={rescanning}
          className="rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-[11.5px] font-semibold text-ink-2 transition-colors enabled:hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {rescanning ? t`rescanning…` : t`Rescan`}
        </button>
      </div>
      {rescanError !== null && (
        <div className="mb-2 font-mono text-[11.5px] text-red" role="alert">
          {t`rescan failed: ${rescanError}`}
        </div>
      )}
      {lastRescan !== null && rescanError === null && (
        <div className="mb-2 font-mono text-[11px] text-ink-dim" role="status">
          {t`rescan: ${lastRescan.mods} mods · ${lastRescan.validated} validated · ${lastRescan.parseErrors} parse errors · ${lastRescan.removed} removed`}
        </div>
      )}
      {error !== null && <ErrorBox message={error} onRetry={refetch} />}
      {error === null && mods === null && <Loading label={t`mods…`} />}
      {error === null && mods !== null && mods.length === 0 && (
        <Empty>
          <Trans>No mods installed</Trans>
        </Empty>
      )}
      {error === null && mods !== null && mods.length > 0 && (
        <>
          <div className="overflow-x-auto rounded-xl border border-line bg-surface">
            <table className="w-full min-w-[720px] border-collapse text-left">
              <caption className="sr-only">{t`Installed mods and their risk badges`}</caption>
              <thead>
                <tr className="border-b border-line font-mono text-[10px] tracking-[0.06em] text-ink-faint uppercase">
                  <th scope="col" className="px-3 py-2 font-normal">{t`name`}</th>
                  <th scope="col" className="px-3 py-2 font-normal">{t`source`}</th>
                  <th scope="col" className="px-3 py-2 font-normal">{t`version`}</th>
                  <th scope="col" className="px-3 py-2 font-normal">{t`events`}</th>
                  <th scope="col" className="px-3 py-2 font-normal">{t`calls`}</th>
                  <th scope="col" className="px-3 py-2 font-normal">{t`risk`}</th>
                </tr>
              </thead>
              <tbody>
                {mods.map((m) => (
                  <ModRow
                    key={m.id}
                    mod={m}
                    selected={m.id === selected}
                    onSelect={() => setSelected((cur) => (cur === m.id ? null : m.id))}
                  />
                ))}
              </tbody>
            </table>
          </div>
          {selected !== null && <ModDetailPanel id={selected} onClose={closeDetail} />}
        </>
      )}
    </div>
  );
}
