// The limits panel beside the sidebar's limits button (Swarmery v4): a sentence on where
// things stand, one tab per subscription, its windows, the projects running as it and
// the way to move one. Everything is real: no window history, per-account spend or
// session count, because the daemon keeps none of those per account (plan D-18).
//
// A card that is not simply fine keeps UsageProviderCard, which owns the setup hint, the
// connect flow and the upstream error. Below `desk` there is no sidebar, and UsageChip
// keeps its UsageModal.
//
// It does not poll: the numbers arrive from the app-wide poller (lib/usageData.tsx). The
// accounts and projects are read on open, and again after a move.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { fetchAccounts, fetchProjects, putProjectAccount } from '../../api';
import type { Account, Project, UsageWindow } from '../../api/types';
import { notifyAccountBindingChanged } from '../../lib/activeAccount';
import { fmtCost, projectLabel } from '../../lib/format';
import {
  type AccountLimit,
  accountLimit,
  type LimitTone,
  limitsSentence,
  limitTone,
  openingAccount,
  paceLine,
  resetsIn,
  resetWhen,
  windowName,
} from '../../lib/limits';
import { usePanels } from '../../lib/panels';
import { useUsage } from '../../lib/usageData';
import { UsageDisconnect } from './UsageDisconnect';
import { fmtClock } from './format';
import { UsageProviderCard } from './UsageProviderCard';

const TICK_MS = 30_000;
const NO_WINDOWS: readonly string[] = [];
const BAR: Record<LimitTone, string> = { red: 'bg-red', brand: 'bg-brand', green: 'bg-green' };
const TEXT: Record<LimitTone, string> = { red: 'text-red', brand: 'text-brand', green: 'text-green' };

export function limitBarClass(percent: number): string {
  return BAR[limitTone(percent)];
}

export function limitTextClass(percent: number): string {
  return TEXT[limitTone(percent)];
}

/** One window as a row: its name, the bar with the time-elapsed tick and the pace line,
 * and when it resets. */
function Window({ window: w, now }: { window: UsageWindow; now: number }): JSX.Element {
  const { t } = useLingui();
  const percent = Math.round(w.percentUsed);
  const used = String(percent);
  const left = String(Math.max(0, Math.round(w.percentLeft)));
  const { name, scope } = windowName(w);
  const pace = paceLine(w);
  const elapsed = w.pace === undefined ? null : Math.min(100, Math.max(0, w.pace.percentElapsed));
  const inText = resetsIn(w, now);
  const when = resetWhen(w, now);
  return (
    <li className="grid grid-cols-[84px_minmax(0,1fr)_auto] items-center gap-3 border-b border-line py-3 last:border-b-0">
      <div className="min-w-0">
        <div className="truncate text-[13px] leading-[normal] font-medium text-ink">{name}</div>
        <div className="mt-0.5 truncate font-mono text-[10.5px] leading-[normal] text-ink-faint">{scope}</div>
      </div>
      <div className="min-w-0 font-mono text-[10.5px] leading-[normal]">
        <div className="flex justify-between gap-2">
          <span className={limitTextClass(percent)}>{t`${used}% used`}</span>
          <span className="text-ink-3">{t`${left}% left`}</span>
        </div>
        <div className="relative mt-1.5">
          <div
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.min(percent, 100)}
            aria-label={`${name} · ${scope}`}
            className="h-1.5 overflow-hidden rounded-[3px] bg-line"
          >
            <div className={`h-full ${limitBarClass(percent)}`} style={{ width: `${String(Math.min(percent, 100))}%` }} />
          </div>
          {elapsed !== null && (
            <span
              aria-hidden="true"
              className="absolute -top-px h-2 w-px bg-ink-3"
              style={{ left: `${elapsed.toFixed(1)}%` }}
            />
          )}
        </div>
        {pace !== null && (
          <div className={`mt-1.5 flex gap-1.5 leading-[1.45] ${TEXT[pace.tone]}`}>
            <span aria-hidden="true" className="shrink-0">
              {pace.glyph}
            </span>
            <span className="min-w-0">{pace.text}</span>
          </div>
        )}
      </div>
      <div className="text-right font-mono text-[10.5px] leading-[normal] whitespace-nowrap">
        {inText !== '' && <div className="text-ink-2">{inText}</div>}
        {when !== '' && <div className="mt-0.5 text-ink-faint">{when}</div>}
      </div>
    </li>
  );
}

interface Binding {
  accounts: Account[];
  projects: Project[];
}

/** The projects an account runs: the ones bound to it, and on the default account every
 * project bound to nothing. */
function projectsOf(binding: Binding, key: string): Project[] {
  const account = binding.accounts.find((a) => a.key === key);
  if (account === undefined) return [];
  const bound = new Set(binding.accounts.flatMap((a) => a.projects));
  return binding.projects.filter((p) => account.projects.includes(p.path) || (account.isDefault && !bound.has(p.path)));
}

function Projects({
  account,
  binding,
  limits,
  onMoved,
}: {
  account: string;
  binding: Binding | null;
  limits: readonly AccountLimit[];
  onMoved: () => void;
}): JSX.Element | null {
  const { t } = useLingui();
  const [open, setOpen] = useState(true);
  const [menu, setMenu] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (binding === null) return null;
  const projects = projectsOf(binding, account);
  const targets = binding.accounts.filter((a) => a.key !== account);
  const move = (project: Project, target: Account): void => {
    setBusy(true);
    setErr(null);
    // '' clears the binding: the default account is no binding at all.
    putProjectAccount(project.id, target.isDefault ? '' : target.key)
      .then(() => {
        setMenu(null);
        notifyAccountBindingChanged();
        onMoved();
      })
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };
  const total = String(binding.projects.length);
  const count = String(projects.length);
  return (
    <div className="border-b border-line">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 bg-transparent px-4 py-2.5 text-left font-mono text-[10.5px] leading-[normal] text-ink-3 transition-colors hover:text-ink"
      >
        <span aria-hidden="true" className="text-[10px] text-ink-faint">
          {open ? '▾' : '▸'}
        </span>
        <span className="min-w-0 truncate">{t`projects running as ${account}`}</span>
        <span className="ml-auto whitespace-nowrap text-ink-faint">{projects.length === 0 ? t`none` : t`${count} of ${total}`}</span>
      </button>
      {open && projects.length > 0 && (
        <ul className="m-0 flex list-none flex-col gap-1.5 p-0 px-4 pb-3">
          {projects.map((project) => (
            <li key={project.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
              <span className="truncate text-[12.5px] leading-[normal] text-ink">{projectLabel(project.name, project.slug)}</span>
              {targets.length > 0 && (
                <span className="relative inline-flex">
                  <button
                    type="button"
                    onClick={() => setMenu((cur) => (cur === project.id ? null : project.id))}
                    aria-expanded={menu === project.id}
                    aria-haspopup="menu"
                    className="rounded-[6px] border border-line-strong bg-transparent px-[9px] py-0.5 font-mono text-[10.5px] leading-[normal] whitespace-nowrap text-ink-3 transition-colors hover:text-ink"
                  >
                    <Trans>move ▾</Trans>
                  </button>
                  {menu === project.id && (
                    <span
                      role="menu"
                      className="absolute top-[calc(100%+4px)] right-0 z-50 flex min-w-40 flex-col rounded-lg border border-line-strong bg-surface p-1 shadow-lg"
                    >
                      {targets.map((target) => {
                        const limit = limits.find((l) => l.name === target.key);
                        const percent = limit?.session == null ? null : Math.round(limit.session.percentUsed);
                        const blocked = target.runnable === false;
                        return (
                          <button
                            key={target.key}
                            type="button"
                            role="menuitem"
                            disabled={busy || blocked}
                            data-tip={blocked ? (target.runnableReason ?? t`this account cannot run`) : undefined}
                            onClick={() => move(project, target)}
                            className="flex items-center gap-2 rounded-[6px] bg-transparent px-2.5 py-1.5 text-left text-[12px] leading-[normal] text-ink transition-colors hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-50"
                          >
                            {t`to ${target.key}`}
                            {percent !== null && (
                              <span className={`ml-auto font-mono text-[10px] ${limitTextClass(percent)}`}>{percent}%</span>
                            )}
                          </button>
                        );
                      })}
                    </span>
                  )}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
      {err !== null && (
        <p role="alert" className="m-0 px-4 pb-3 font-mono text-[10.5px] leading-[1.5] text-red">
          {err}
        </p>
      )}
    </div>
  );
}

export function LimitsPanel({
  onClose,
  activeAccount,
  spentToday,
}: {
  onClose: () => void;
  /** The account the scoped project runs under, when there is a scope. */
  activeAccount: string | null;
  spentToday: number | null;
}): JSX.Element {
  const { t } = useLingui();
  const { accounts, error, loading, lastUpdated, refresh } = useUsage();
  const { nav: collapsed } = usePanels();
  const limits = useMemo(() => accounts.map(accountLimit), [accounts]);
  const [tab, setTab] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [binding, setBinding] = useState<Binding | null>(null);
  const dialogRef = useRef<HTMLDivElement>(null);

  const loadBinding = useCallback((): void => {
    Promise.all([fetchAccounts(), fetchProjects()])
      .then(([a, projects]) => setBinding({ accounts: a.accounts, projects }))
      .catch(() => setBinding(null)); // the numbers stand on their own
  }, []);
  useEffect(loadBinding, [loadBinding]);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), TICK_MS);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [onClose]);

  useEffect(() => {
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialogRef.current?.focus();
    return () => trigger?.focus();
  }, []);

  const shown = limits.find((l) => l.name === tab) ?? limits.find((l) => l.name === openingAccount(limits, activeAccount));
  const others = accounts.find((a) => a.account === shown?.name)?.providers.filter((p) => p !== shown?.provider) ?? [];
  const provider = shown?.provider ?? null;
  const updated = lastUpdated === null ? '' : fmtClock(lastUpdated);

  return (
    <>
      <div aria-hidden="true" className="fixed inset-0 z-30" onClick={onClose} />
      <div
        ref={dialogRef}
        role="dialog"
        tabIndex={-1}
        aria-label={t`Subscription limits`}
        style={{ left: collapsed ? 62 : 206 }}
        className="fixed bottom-2.5 z-40 max-h-[calc(100vh-24px)] w-[380px] max-w-[calc(100vw-240px)] overflow-y-auto rounded-xl border border-line-strong bg-surface shadow-[0_18px_50px_rgba(0,0,0,0.45)] outline-none"
      >
        <div className="flex items-center gap-2 border-b border-line bg-surface2 px-4 py-3">
          <span className="font-mono text-[10px] leading-[normal] tracking-[0.12em] text-ink-faint uppercase">
            <Trans>limits</Trans>
          </span>
          <span className="min-w-0 text-[12.5px] leading-[1.4] text-ink-2">{limitsSentence(limits, now)}</span>
          <button
            type="button"
            onClick={onClose}
            aria-label={t`Close limits`}
            className="ml-auto bg-transparent px-0.5 text-[14px] leading-[normal] text-ink-faint transition-colors hover:text-ink"
          >
            ×
          </button>
        </div>

        {limits.length > 1 && (
          <div role="tablist" aria-label={t`usage accounts`} className="flex gap-0.5 border-b border-line bg-surface2 px-2.5 py-1.5">
            {limits.map((limit) => {
              const on = limit.name === shown?.name;
              const percent = limit.session === null ? null : Math.round(limit.session.percentUsed);
              return (
                <button
                  key={limit.name}
                  type="button"
                  role="tab"
                  aria-selected={on}
                  onClick={() => setTab(limit.name)}
                  className={`inline-flex items-center gap-1.5 rounded-[6px] border px-2.5 py-1 text-[12px] leading-[normal] font-medium transition-colors ${
                    on ? 'border-line-strong bg-surface text-ink' : 'border-transparent bg-transparent text-ink-3 hover:text-ink'
                  }`}
                >
                  <span
                    aria-hidden="true"
                    className={`h-[7px] w-[7px] shrink-0 rounded-full ${percent === null ? 'bg-line-strong' : limitBarClass(percent)}`}
                  />
                  {limit.name}
                  {percent !== null && <span className={`font-mono text-[10px] ${limitTextClass(percent)}`}>{percent}%</span>}
                </button>
              );
            })}
          </div>
        )}

        {shown !== undefined && (
          <div className="border-b border-line px-4 py-3">
            {shown.windows.length > 0 ? (
              <>
                <div className="flex items-center gap-2 font-mono text-[10.5px] leading-[normal]">
                  <span className={shown.state === 'limit' ? 'text-brand' : 'text-green'}>
                    {shown.state === 'limit' ? t`paused` : t`active`}
                  </span>
                  {provider?.plan !== undefined && <span className="text-ink-faint">{provider.plan}</span>}
                  {provider?.connectedVia === 'swarmery' && (
                    <span className="ml-auto">
                      <UsageDisconnect account={provider.account} />
                    </span>
                  )}
                </div>
                <ul className="m-0 mt-1 list-none p-0">
                  {shown.windows.map((w) => (
                    <Window key={w.key} window={w} now={now} />
                  ))}
                </ul>
              </>
            ) : (
              provider !== null && (
                <UsageProviderCard
                  p={provider}
                  showAccount={false}
                  active={false}
                  mode="used"
                  hidden={[...NO_WINDOWS]}
                  onToggleWindow={() => undefined}
                  onShowAllHidden={() => undefined}
                  now={now}
                />
              )
            )}
            {provider === null && shown.windows.length === 0 && (
              <p className="m-0 font-mono text-[10.5px] leading-[1.5] text-ink-faint">
                <Trans>no numbers for this account</Trans>
              </p>
            )}
            {others.map((p) => (
              <div key={`${p.account}:${p.name}`} className="mt-2.5">
                <UsageProviderCard
                  p={p}
                  showAccount={false}
                  active={false}
                  mode="used"
                  hidden={[...NO_WINDOWS]}
                  onToggleWindow={() => undefined}
                  onShowAllHidden={() => undefined}
                  now={now}
                />
              </div>
            ))}
          </div>
        )}

        {shown !== undefined && <Projects account={shown.name} binding={binding} limits={limits} onMoved={loadBinding} />}

        <div className="flex items-center gap-3.5 px-4 py-2.5 font-mono text-[10.5px] leading-[normal] text-ink-3">
          <span>
            <Trans>
              today <span className="text-ink">{fmtCost(spentToday)}</span>
            </Trans>
          </span>
          <span className="min-w-0 truncate text-ink-faint">
            {lastUpdated === null ? t`not yet loaded` : t`updated ${updated}`}
            {error !== null && <span className="text-red">{t` · refresh failed`}</span>}
          </span>
          <button
            type="button"
            onClick={() => void refresh(true)}
            disabled={loading}
            className="ml-auto bg-transparent p-0 text-ink-faint transition-colors hover:text-brand disabled:opacity-50"
          >
            {loading ? '…' : t`refresh`}
          </button>
          <Link to="/settings?tab=accounts" onClick={onClose} className="whitespace-nowrap text-ink-faint transition-colors hover:text-brand">
            <Trans>accounts →</Trans>
          </Link>
        </div>
      </div>
    </>
  );
}
