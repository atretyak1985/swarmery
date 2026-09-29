// AccountsSection — the operator's Claude accounts (multi-account, phase 7):
// self-fetching global list, matching ConnectorsSection's state shape (see
// that component for the loading/error/empty pattern this mirrors).
//
// `connected` is THREE states, not two: true/false = the question was asked
// and answered, null = it could not be asked at all (SWARMERY_USAGE_OAUTH=0).
// Rendering null as "not connected" would misstate the operator's real
// subscription state, so every state gets its own dot AND its own text — a
// coloured dot alone is never the only signal (accessibility).
//
// The default account has no delete button at all (not a disabled one): it
// is the account swarmery falls back to when a project has no explicit
// binding, and there is nothing meaningful to remove it to. Deleting any
// other account can leave projects still pointing at it via their stored
// project.json binding; the server reports those back as `danglingBindings`,
// and that banner is dismiss-only — it must never disappear on its own while
// the operator hasn't acknowledged it.

import { useEffect, useState } from 'react';
import { deleteAccount, fetchAccounts, probeAccount } from '../api';
import type { Account, AccountProbeResponse, IgnoredBinding } from '../api/types';
import { applyVerdict, patchReadiness } from '../lib/accountReadiness';
import { fmtAgo } from '../lib/format';
import { CreateAccountModal } from './CreateAccountModal';
import { ConfirmDialog, Empty, ErrorBox, Loading } from './ui';

type State =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; accounts: Account[]; ignored: IgnoredBinding[] };

type ConnectedState = 'yes' | 'no' | 'unknown';

const CONNECTED_DOT: Record<ConnectedState, string> = {
  yes: 'bg-green',
  no: 'bg-red',
  unknown: 'bg-line',
};

const CONNECTED_LABEL: Record<ConnectedState, string> = {
  yes: 'connected',
  no: 'not connected',
  unknown: 'connection status unknown',
};

function connectedState(connected: boolean | null): ConnectedState {
  if (connected === true) return 'yes';
  if (connected === false) return 'no';
  return 'unknown';
}

/** Hairline pill, matching ConnectorsSection's Chip styling. */
function Pill({ children }: { children: string }): JSX.Element {
  return (
    <span className="shrink-0 rounded-full border border-line px-1.5 py-0.5 font-mono text-[9.5px] whitespace-nowrap text-ink-dim">
      {children}
    </span>
  );
}

function ConnectedBadge({ connected }: { connected: boolean | null }): JSX.Element {
  const state = connectedState(connected);
  return (
    <span className="flex shrink-0 items-center gap-1">
      <span
        aria-hidden="true"
        className={`inline-block h-[7px] w-[7px] rounded-full ${CONNECTED_DOT[state]}`}
      />
      <span className="font-mono text-[9.5px] text-ink-dim">{CONNECTED_LABEL[state]}</span>
    </span>
  );
}

/**
 * The READINESS dimension, separate from `connected`: whether `claude` can
 * actually run under this account (the stored probe verdict). Same rule as
 * the badge above — a coloured dot is never the only signal, every rendered
 * state carries its own text. `null` (never probed) renders NOTHING: the
 * question was not answered, and an unknown is not a failure.
 */
function ReadyBadge({ a }: { a: Account }): JSX.Element | null {
  if (a.runnable === null) return null;
  const tip =
    a.runnableCheckedAt !== undefined ? `checked ${fmtAgo(a.runnableCheckedAt)}` : undefined;
  return (
    <span className="flex shrink-0 items-center gap-1" data-tip={tip}>
      <span
        aria-hidden="true"
        className={`inline-block h-[7px] w-[7px] rounded-full ${a.runnable ? 'bg-green' : 'bg-amber'}`}
      />
      <span className={`font-mono text-[9.5px] ${a.runnable ? 'text-ink-dim' : 'text-amber'}`}>
        {a.runnable ? 'ready' : (a.runnableReason ?? 'CLI login required')}
      </span>
    </span>
  );
}

function AccountRow({
  a,
  probing,
  onProbe,
  onRequestDelete,
}: {
  a: Account;
  probing: boolean;
  onProbe: (key: string) => void;
  onRequestDelete: (key: string) => void;
}): JSX.Element {
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5 px-3.5 py-2.5">
      <ConnectedBadge connected={a.connected} />
      <ReadyBadge a={a} />

      <span className="min-w-0 truncate font-mono text-[12px] text-ink" title={a.key}>
        {a.key}
      </span>

      {a.isDefault && <Pill>default</Pill>}
      {a.plan !== '' && <Pill>{a.plan}</Pill>}

      <span
        className="order-last basis-full truncate font-mono text-[11px] text-ink-dim sm:order-none sm:min-w-0 sm:flex-1 sm:basis-0"
        title={a.configDir}
      >
        {a.configDir}
      </span>

      {/* Re-run the CLI-readiness probe — the only way a never-probed account
          gets a verdict without a dispatch, and the honest refresh after a
          terminal login the daemon did not witness. */}
      <button
        type="button"
        onClick={() => onProbe(a.key)}
        disabled={probing}
        className="ml-auto shrink-0 rounded border border-line px-1.5 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:bg-surface2 disabled:opacity-50"
      >
        {probing ? 'checking…' : 'check now'}
      </button>

      {!a.isDefault && (
        <button
          type="button"
          onClick={() => onRequestDelete(a.key)}
          className="shrink-0 rounded border border-line px-1.5 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:border-red/40 hover:text-red"
        >
          remove
        </button>
      )}

      <BoundProjects a={a} />
    </div>
  );
}

// BoundProjects lists the paths bound to an account: indexed ones (a live
// projects row) and, as a separate labelled group, bound paths with NO live
// row — visible, never presented as indexed (SC-10).
function BoundProjects({ a }: { a: Account }): JSX.Element | null {
  const unindexed = a.projectsUnindexed ?? [];
  if (a.projects.length === 0 && unindexed.length === 0) return null;
  return (
    <div className="order-last basis-full space-y-1 pt-1">
      {a.projects.length > 0 && (
        <PathGroup label={`bound projects (${String(a.projects.length)})`} paths={a.projects} />
      )}
      {unindexed.length > 0 && (
        <PathGroup
          label={`bound, not indexed (${String(unindexed.length)}) — no live project row`}
          paths={unindexed}
        />
      )}
    </div>
  );
}

function PathGroup({ label, paths }: { label: string; paths: string[] }): JSX.Element {
  return (
    <div>
      <div className="font-mono text-[10px] text-ink-dim">{label}</div>
      <ul className="space-y-0.5">
        {paths.map((p) => (
          <li key={p} className="truncate font-mono text-[10.5px] text-ink-2" title={p}>
            {p}
          </li>
        ))}
      </ul>
    </div>
  );
}

// IgnoredBindings lists binding files the read side ignores (git-tracked,
// indeterminate, or an untrusted mode/owner/type) with their reason — shown as
// ignored instead of silently dropped, and counted under no account.
function IgnoredBindings({ items }: { items: IgnoredBinding[] }): JSX.Element | null {
  if (items.length === 0) return null;
  return (
    <div className="mt-2.5 rounded-xl border border-amber/25 bg-amber/5 px-3.5 py-3">
      <div className="font-mono text-[11.5px] text-amber">
        ignored bindings ({String(items.length)}) — counted under no account
      </div>
      <ul className="mt-1.5 space-y-1.5">
        {items.map((b) => (
          <li key={b.path} className="font-mono text-[10.5px]">
            <div className="truncate text-ink-2" title={b.path}>
              {b.path}
              {b.declares !== '' && <span className="text-ink-dim"> — declares {b.declares}</span>}
            </div>
            <div className="break-words text-ink-dim">{b.reason}</div>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function AccountsSection(): JSX.Element {
  const [state, setState] = useState<State>({ kind: 'loading' });
  const [retry, setRetry] = useState(0);
  const [modalOpen, setModalOpen] = useState(false);
  const [confirmKey, setConfirmKey] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [probingKey, setProbingKey] = useState<string | null>(null);
  const [probeError, setProbeError] = useState<string | null>(null);
  // Dangling-bindings banner is intentionally decoupled from `state`/`retry`:
  // a refetch after deletion must not make it vanish before the operator
  // dismisses it themselves.
  const [dangling, setDangling] = useState<string[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    setState({ kind: 'loading' });

    void fetchAccounts()
      .then((resp) => {
        if (!cancelled)
          setState({ kind: 'ready', accounts: resp.accounts, ignored: resp.ignoredBindings ?? [] });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setState({
          kind: 'error',
          message: err instanceof Error ? err.message : 'could not read accounts',
        });
      });

    return () => {
      cancelled = true;
    };
  }, [retry]);

  function requestDelete(key: string): void {
    setDeleteError(null);
    setConfirmKey(key);
  }

  async function probe(key: string): Promise<void> {
    setProbingKey(key);
    setProbeError(null);
    try {
      const verdict: AccountProbeResponse = await probeAccount(key);
      setState((s) =>
        s.kind === 'ready'
          ? {
              ...s,
              accounts: s.accounts.map((a) => (a.key === key ? applyVerdict(a, verdict) : a)),
            }
          : s,
      );
      // Keep the header surfaces (banner, chip) in step with the new verdict.
      patchReadiness(key, verdict);
    } catch (e) {
      setProbeError(e instanceof Error ? e.message : String(e));
    } finally {
      setProbingKey(null);
    }
  }

  async function confirmDelete(): Promise<void> {
    const key = confirmKey;
    if (key === null) return;
    setDeleting(true);
    setDeleteError(null);
    try {
      const resp = await deleteAccount(key);
      if (resp.danglingBindings !== undefined && resp.danglingBindings.length > 0) {
        setDangling(resp.danglingBindings);
      }
      setConfirmKey(null);
      setRetry((n) => n + 1);
    } catch (e) {
      setDeleteError(e instanceof Error ? e.message : String(e));
    } finally {
      setDeleting(false);
    }
  }

  return (
    <>
      {dangling !== null && dangling.length > 0 && (
        <div
          className="mb-2.5 rounded-lg border border-amber/25 bg-amber/5 px-3.5 py-3"
          role="alert"
        >
          <div className="flex items-start justify-between gap-2">
            <div className="font-mono text-[11.5px] text-amber">
              removed account still bound in{' '}
              {dangling.length === 1 ? '1 project' : `${String(dangling.length)} projects`}
            </div>
            <button
              type="button"
              onClick={() => setDangling(null)}
              className="shrink-0 rounded border border-line px-1.5 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:bg-surface2"
            >
              dismiss
            </button>
          </div>
          <ul className="mt-1.5 space-y-0.5">
            {dangling.map((path) => (
              <li
                key={path}
                className="truncate font-mono text-[10.5px] text-ink-dim"
                title={path}
              >
                {path}
              </li>
            ))}
          </ul>
        </div>
      )}

      {state.kind === 'loading' && <Loading label="reading accounts…" />}

      {state.kind === 'error' && (
        <ErrorBox
          message={state.message}
          onRetry={() => {
            setRetry((n) => n + 1);
          }}
        />
      )}

      {state.kind === 'ready' && state.accounts.length === 0 && (
        <Empty>no accounts configured</Empty>
      )}

      {state.kind === 'ready' && probeError !== null && (
        <div className="mb-2 font-mono text-[11px] break-words text-red" role="alert">
          readiness check failed: {probeError}
        </div>
      )}

      {state.kind === 'ready' && state.accounts.length > 0 && (
        <div className="divide-y divide-line rounded-xl border border-line bg-surface">
          {state.accounts.map((a) => (
            <AccountRow
              key={a.key}
              a={a}
              probing={probingKey === a.key}
              onProbe={(key) => void probe(key)}
              onRequestDelete={requestDelete}
            />
          ))}
        </div>
      )}

      {state.kind === 'ready' && <IgnoredBindings items={state.ignored} />}

      <div className="mt-2 flex justify-end">
        <button
          type="button"
          onClick={() => setModalOpen(true)}
          className="rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-[11px] text-ink-2 transition-colors hover:bg-surface2"
        >
          + add account
        </button>
      </div>

      <CreateAccountModal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
        onCreated={() => {
          setModalOpen(false);
          setRetry((n) => n + 1);
        }}
      />

      <ConfirmDialog
        open={confirmKey !== null}
        title="Remove account"
        confirmLabel="remove"
        danger
        busy={deleting}
        onConfirm={() => void confirmDelete()}
        onCancel={() => {
          if (!deleting) setConfirmKey(null);
        }}
      >
        {confirmKey !== null && (
          <>
            Remove account <span className="font-mono text-ink">{confirmKey}</span>? Projects
            explicitly bound to it will fall back to the default account.
          </>
        )}
        {deleteError !== null && (
          <div className="mt-2 font-mono text-[11px] text-red">{deleteError}</div>
        )}
      </ConfirmDialog>
    </>
  );
}
