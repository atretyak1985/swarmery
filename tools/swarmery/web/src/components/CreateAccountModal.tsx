// Create-account modal (multi-account, phase 7). Two stages: a form for the
// account key, then — on success — the connect stage. The PRIMARY path is the
// one-click Connect (the daemon's own OAuth flow, credential handoff and CLI
// probe included — UsageConnect); the CLI command stays as the documented
// manual alternative. The modal still never runs `loginCommand` itself: the
// server only reserves a config dir, it does not (and cannot) drive the
// operator's own `claude` login flow. Pretending success here would be a lie
// about whether the account is actually usable.

import { useId, useState } from 'react';
import { createAccount } from '../api';
import { refreshReadiness } from '../lib/accountReadiness';
import { ExplainPair } from './Explain';
import { TerminalPathNote } from './TerminalPathNote';
import { UsageConnect } from './usage/UsageConnect';
import { ConfirmDialog, ErrorBox } from './ui';
import { useDiscardGuard } from './useDiscardGuard';

type Stage =
  | { kind: 'form' }
  | { kind: 'saving' }
  | { kind: 'done'; loginCommand: string; hint: string | undefined };

const KEY_HINT = "letters, digits, '-' or '_' (it becomes a directory name)";

export function CreateAccountModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}): JSX.Element | null {
  const titleId = useId();
  const [key, setKey] = useState('');
  const [stage, setStage] = useState<Stage>({ kind: 'form' });
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  // The embedded Connect came out CLI-ready — swap the flow for the terminal
  // path note. The manual command stays visible until then.
  const [resolved, setResolved] = useState(false);

  function reset(): void {
    setKey('');
    setStage({ kind: 'form' });
    setError(null);
    setCopied(false);
    setResolved(false);
  }

  function resetAndClose(): void {
    reset();
    onClose();
  }

  // Dirty = the key differs from the empty value the form opens with. Called
  // before the `open` early return (hook order); the guard clears its own
  // confirm on discard, so a reopened modal starts on a clean form.
  const busy = stage.kind === 'saving';
  const discard = useDiscardGuard(stage.kind === 'form' && key.trim() !== '', resetAndClose, {
    disabled: busy,
  });

  if (!open) return null;

  function onKeyDown(e: React.KeyboardEvent<HTMLDivElement>): void {
    if (e.key === 'Escape') discard.requestClose();
  }

  async function submit(): Promise<void> {
    if (key.trim() === '') return;
    setStage({ kind: 'saving' });
    setError(null);
    try {
      const resp = await createAccount(key.trim());
      setStage({ kind: 'done', loginCommand: resp.loginCommand, hint: resp.hint });
    } catch (e) {
      setStage({ kind: 'form' });
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  function copy(loginCommand: string): void {
    void navigator.clipboard.writeText(loginCommand);
    setCopied(true);
  }

  function done(): void {
    reset();
    onCreated();
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      onClick={discard.requestClose}
      onKeyDown={onKeyDown}
    >
      <div
        className="w-full max-w-md rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div id={titleId} className="font-display text-[14px] font-bold text-ink">
          <ExplainPair id="claude-account">Add Claude account</ExplainPair>
        </div>

        {stage.kind !== 'done' ? (
          <form
            className="mt-3"
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            <label
              htmlFor="account-key"
              className="block font-mono text-[10.5px] tracking-[0.12em] text-ink-dim uppercase"
            >
              account key
            </label>
            <input
              id="account-key"
              type="text"
              autoFocus
              value={key}
              disabled={busy}
              onChange={(e) => setKey(e.target.value)}
              aria-describedby="account-key-hint"
              className="mt-1 w-full rounded-lg border border-line bg-bg px-2.5 py-1.5 font-mono text-[12.5px] text-ink outline-none focus:border-line-strong"
            />
            <p id="account-key-hint" className="mt-1 text-[10.5px] text-ink-faint">
              {KEY_HINT}
            </p>

            {error !== null && <ErrorBox message={error} />}

            <div className="mt-4 flex justify-end gap-2">
              <button
                type="button"
                onClick={discard.requestClose}
                disabled={busy}
                className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
              >
                cancel
              </button>
              <button
                type="submit"
                disabled={busy || key.trim() === ''}
                className="rounded-lg border border-green/40 bg-green/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
              >
                {busy ? '…' : 'create'}
              </button>
            </div>
          </form>
        ) : (
          <div className="mt-3">
            {resolved ? (
              <div>
                <p className="text-[12px] leading-relaxed text-ink-2">
                  Account <span className="font-mono text-ink">{key.trim()}</span> is connected
                  and CLI-ready.
                </p>
                <TerminalPathNote />
              </div>
            ) : (
              <>
                <p className="text-[12px] leading-relaxed text-ink-2">
                  Account <span className="font-mono text-ink">{key.trim()}</span> reserved.
                  Connect it here — one authorization covers the quota read, the CLI
                  credential, and the readiness check:
                </p>
                {/* The one-click flow, including its pty-login fallback step. */}
                <UsageConnect
                  account={key.trim()}
                  onResolved={() => {
                    setResolved(true);
                    void refreshReadiness();
                  }}
                />
                <p className="mt-3 text-[11px] leading-relaxed text-ink-dim">
                  Prefer the terminal? Run this yourself — swarmery never runs it for you:
                </p>
                <div className="mt-1.5 flex items-center gap-2 rounded-lg border border-line bg-bg px-2.5 py-1.5">
                  <code className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-ink">
                    {stage.loginCommand}
                  </code>
                  <button
                    type="button"
                    onClick={() => copy(stage.loginCommand)}
                    className="shrink-0 rounded border border-line px-1.5 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:bg-surface2"
                  >
                    {copied ? 'copied' : 'copy'}
                  </button>
                </div>
                <p className="mt-1.5 font-mono text-[10px] leading-relaxed text-ink-faint">
                  on macOS the CLI stores a non-default account&apos;s login in the login
                  Keychain (no credentials file) — the dashboard reads it from there, so press
                  &ldquo;check now&rdquo; on the account after logging in
                </p>
              </>
            )}
            {stage.hint !== undefined && stage.hint !== '' && (
              <p className="mt-2 font-mono text-[10.5px] text-amber">{stage.hint}</p>
            )}
            <div className="mt-4 flex justify-end">
              <button
                type="button"
                onClick={done}
                className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2"
              >
                done
              </button>
            </div>
          </div>
        )}
      </div>

      <ConfirmDialog
        {...discard.confirmProps}
        title="Discard account key?"
        confirmLabel="discard"
        danger
      >
        The account key you typed will be lost.
      </ConfirmDialog>
    </div>
  );
}
