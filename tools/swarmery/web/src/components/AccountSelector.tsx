// Account selector (/p/:slug/settings — fusion phase 7): lets a project pick
// which provisioned account a dispatched process runs under, or fall back to
// the machine's default account. Renders nothing on a single-account (or
// zero-account) machine — the control must be invisible there, not just
// disabled, so an unaffected project's settings page looks exactly as it did
// before this feature shipped.
//
// Race-condition note (plan risk table, "switched mid-run"): the account is
// resolved once, at process spawn time. Changing the binding here never
// touches an already-running dispatcher process — it only takes effect on
// the NEXT run. The UI hint below is the documented mitigation, not decor.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { Account, AccountBinding } from '../api/types';
import { fetchAccounts, fetchProjectAccount, putProjectAccount } from '../api';
import { notifyAccountBindingChanged } from '../lib/activeAccount';
import { ExplainPair } from './Explain';
import { Card, ErrorBox, SectionTitle } from './ui';

interface AccountOption {
  /** '' selects "default" — clears the project's explicit binding. */
  value: string;
  label: string;
  detail: string;
}

/** "connected" (quota readable) and "ready" (CLI can run) are DIFFERENT
 * questions, each tri-state — speak them separately, and never render a null
 * as a failure: an unasked question is silence, not a state. */
function statusBits(account: Account): string[] {
  const bits: string[] = [];
  if (account.connected === true) bits.push('connected');
  else if (account.connected === false) bits.push('not connected');
  else bits.push('connection unknown');
  if (account.runnable === true) bits.push('ready');
  else if (account.runnable === false)
    bits.push(account.runnableReason ?? 'CLI login required');
  return bits;
}

function buildOptions(accounts: readonly Account[]): AccountOption[] {
  const defaultAccount = accounts.find((a) => a.isDefault);
  const defaultLabel = defaultAccount !== undefined ? defaultAccount.key : 'unset';
  const options: AccountOption[] = [
    {
      value: '',
      label: `Default (${defaultLabel})`,
      detail: 'follow whichever account is marked default on this machine',
    },
  ];
  for (const account of accounts) {
    const bits: string[] = [];
    if (account.isDefault) bits.push('default');
    bits.push(account.plan !== '' ? account.plan : 'plan unknown');
    bits.push(...statusBits(account));
    options.push({ value: account.key, label: account.key, detail: bits.join(' · ') });
  }
  return options;
}

export function AccountSelector({ projectId }: { projectId: number }): JSX.Element | null {
  const [accounts, setAccounts] = useState<Account[] | null>(null);
  const [binding, setBinding] = useState<AccountBinding | null>(null);
  const [draft, setDraft] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const aliveRef = useRef(true);

  const load = useCallback((): void => {
    setError(null);
    Promise.all([fetchAccounts(), fetchProjectAccount(projectId)])
      .then(([accountsRes, bindingRes]) => {
        if (!aliveRef.current) return;
        setAccounts(accountsRes.accounts);
        setBinding(bindingRes);
        setDraft(bindingRes.account);
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return;
        setError(e instanceof Error ? e.message : String(e));
      });
  }, [projectId]);

  useEffect(() => {
    aliveRef.current = true;
    setAccounts(null);
    setBinding(null);
    setDraft(null);
    load();
    return () => {
      aliveRef.current = false;
    };
  }, [load]);

  const save = (value: string): void => {
    setBusy(true);
    putProjectAccount(projectId, value)
      .then((b) => {
        if (!aliveRef.current) return;
        setBinding(b);
        setDraft(b.account);
        setError(null);
        // The banner and the chip read the binding through their own hook —
        // tell them it moved, or they stay honest-but-stale until a scope change.
        notifyAccountBindingChanged();
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return;
        setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (aliveRef.current) setBusy(false);
      });
  };

  const reset = (): void => {
    if (binding === null) return;
    setDraft(binding.account);
    setError(null);
  };

  // A fetch failure before we ever learned the account count. We can't yet
  // tell whether this project even qualifies for the picker, but silently
  // eating a real error would be worse than a rare card on a single-account
  // box that happened to hit a transient failure.
  if (error !== null && accounts === null) {
    return (
      <>
        <SectionTitle>
        <ExplainPair id="account-binding">account</ExplainPair>
      </SectionTitle>
        <ErrorBox message={error} onRetry={load} />
      </>
    );
  }

  // Still loading, or resolved to a machine with fewer than two accounts:
  // render nothing. This keeps a single-account project's settings page
  // pixel-identical to how it looked before this control existed.
  if (accounts === null || binding === null || draft === null) return null;
  if (accounts.length < 2) return null;

  const options = buildOptions(accounts);
  const dirty = draft !== binding.account;
  const sourceLabel = binding.source === 'binding' ? 'explicit binding' : 'default account';
  // The account the CURRENT draft resolves to ('' follows the default), so the
  // warning below fires for "Default" too when the default itself is not
  // ready. Only an answered "no" warns — runnable null is an unasked question.
  const draftAccount = accounts.find((a) => (draft === '' ? a.isDefault : a.key === draft));
  const draftNotReady = draftAccount?.runnable === false;

  return (
    <>
      <SectionTitle>
        <ExplainPair id="account-binding">account</ExplainPair>
      </SectionTitle>
      {error !== null && (
        <div className="mb-2">
          <ErrorBox message={error} onRetry={reset} />
        </div>
      )}
      <Card>
        <div className="flex flex-wrap items-center gap-2 font-mono text-[11.5px]">
          <span className="text-ink-dim">running as</span>
          <span
            className="font-semibold text-ink"
            data-tip-mono
            data-tip={binding.configDir}
          >
            {binding.effective}
          </span>
          <span className="rounded-full border border-line px-2.5 py-0.5 text-[10px] text-ink-faint">
            {sourceLabel}
          </span>
        </div>
        {binding.ignoredReason && (
          <p className="mt-2 text-[11.5px] leading-snug text-ink-dim">
            binding ignored: {binding.ignoredReason}
          </p>
        )}

        <div className="mt-3 flex flex-col gap-2">
          {options.map((option) => (
            <label
              key={option.value === '' ? '__default__' : option.value}
              className={`flex cursor-pointer gap-2.5 rounded-xl border px-3.5 py-3 transition-colors ${
                draft === option.value ? 'border-brand/50 bg-brand/5' : 'border-line hover:bg-surface2'
              } ${busy ? 'cursor-not-allowed opacity-60' : ''}`}
            >
              <input
                type="radio"
                name="project-account"
                checked={draft === option.value}
                disabled={busy}
                onChange={() => setDraft(option.value)}
                className="mt-0.5 accent-brand focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
              />
              <span className="min-w-0">
                <span className="block font-mono text-[12px] font-semibold text-ink">{option.label}</span>
                <span className="mt-0.5 block text-[11.5px] leading-snug text-ink-dim">
                  {option.detail}
                </span>
              </span>
            </label>
          ))}
        </div>

        <div className="mt-4 flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={!dirty || busy}
            onClick={() => save(draft)}
            className="rounded-lg border border-brand/45 bg-brand/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
          >
            {busy ? 'saving…' : 'save'}
          </button>
          {dirty && (
            <button
              type="button"
              disabled={busy}
              onClick={reset}
              className="rounded-lg border border-line-strong px-3 py-1.5 font-mono text-[11.5px] text-ink-3 transition-colors hover:bg-surface2 disabled:opacity-50"
            >
              reset
            </button>
          )}
          {draftNotReady && draftAccount !== undefined && (
            <span role="alert" className="font-mono text-[10.5px] leading-snug text-amber">
              {draftAccount.key} is not ready —{' '}
              {draftAccount.runnableReason ?? 'its CLI login is missing'}; a run dispatched
              under it will fail at spawn until it is connected (Settings → accounts)
            </span>
          )}
        </div>
        <div className="mt-2 font-mono text-[10px] text-ink-faint">
          the account binds when a dispatcher process spawns — a run already in flight keeps its old
          account until it finishes; switching here only affects the next run
        </div>
      </Card>
    </>
  );
}
