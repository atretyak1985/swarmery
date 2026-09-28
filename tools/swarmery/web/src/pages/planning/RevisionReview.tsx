// Revision review (plan-revision phase 4): the staged diff of one plan
// revision — reason/origin header, one collapsible block per file with an
// action chip and the unified diff (rendered by the shared DiffBlock, which is
// also what the daemon's detail endpoint semantics assume: the diff is against
// the LIVE doc at fetch time), and the Apply / Reject decision footer.
//
// Two server-truth rules this view enforces:
//   • a `stale` file (live content drifted since staging) blocks Apply until
//     the view is reloaded — the diff on screen would not be the diff applied;
//   • an Apply 409 renders the server's conflict rows (doc + drift diff) with
//     a Reload action instead of a bare error string.

import { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import type { PlanRevision, RevisionAction, RevisionConflict } from '../../api/types';
import { applyRevision, fetchRevision, rejectRevision, type RevisionApplyError } from '../../api';
import { fmtDateTime } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { ConfirmDialog, ErrorBox, Loading } from '../../components/ui';
import { useDiscardGuard } from '../../components/useDiscardGuard';
import { DiffBlock } from '../system/ItemDetail';

const ACTION_CHIP: Record<RevisionAction, string> = {
  create: 'border-green/40 bg-green/10 text-green',
  update: 'border-brand/40 bg-brand/10 text-brand',
  rename: 'border-amber/40 bg-amber/10 text-amber',
  delete: 'border-red/40 bg-red/10 text-red',
};

export const ORIGIN_LABEL: Record<PlanRevision['origin'], string> = {
  operator_revise: 'Operator',
  phase_diagnosis: 'From phase diagnosis',
};

export function RevisionReview({
  revisionId,
  onDecided,
}: {
  revisionId: number;
  /** A decision landed (applied or rejected) — the caller refetches its list. */
  onDecided: () => void;
}): JSX.Element {
  const sessionHref = useSessionHref();
  const [rev, setRev] = useState<PlanRevision | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [rejectOpen, setRejectOpen] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  // The Apply 409 body: every doc whose live content drifted since staging.
  const [conflicts, setConflicts] = useState<RevisionConflict[] | null>(null);

  const load = useCallback((): void => {
    setRev(null);
    setError(null);
    setActionErr(null);
    setConflicts(null);
    fetchRevision(revisionId)
      .then(setRev)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [revisionId]);

  useEffect(() => {
    load();
  }, [load]);

  if (error !== null) return <ErrorBox message={error} onRetry={load} />;
  if (rev === null) return <Loading label="revision…" />;

  const anyStale = rev.files.some((f) => f.stale === true);
  const decided = rev.status !== 'staged';

  const apply = (): void => {
    setBusy(true);
    setActionErr(null);
    setConflicts(null);
    applyRevision(revisionId)
      .then(() => {
        setConfirmOpen(false);
        onDecided();
        load();
      })
      .catch((e: unknown) => {
        setConfirmOpen(false);
        setActionErr(e instanceof Error ? e.message : String(e));
        const c = (e as RevisionApplyError).conflicts;
        if (c !== undefined && c.length > 0) setConflicts(c);
      })
      .finally(() => setBusy(false));
  };

  const reject = (note: string): void => {
    setBusy(true);
    setActionErr(null);
    rejectRevision(revisionId, note.trim() === '' ? undefined : note.trim())
      .then(() => {
        setRejectOpen(false);
        onDecided();
        load();
      })
      .catch((e: unknown) => {
        setRejectOpen(false);
        setActionErr(e instanceof Error ? e.message : String(e));
      })
      .finally(() => setBusy(false));
  };

  return (
    <div className="space-y-3">
      {/* Header: why, who asked, when, and the wizard session behind it. */}
      <div>
        <div className="flex flex-wrap items-center gap-1.5">
          <span
            className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${
              rev.origin === 'phase_diagnosis'
                ? 'border-amber/40 bg-amber/10 text-amber'
                : 'border-line-strong bg-surface2 text-ink-dim'
            }`}
          >
            {ORIGIN_LABEL[rev.origin]}
          </span>
          <span className="font-mono text-[10px] text-ink-faint">
            staged {fmtDateTime(rev.createdAt)}
          </span>
          {rev.sessionUuid !== undefined && rev.sessionUuid !== '' && (
            <Link
              to={sessionHref(rev.sessionUuid)}
              className="font-mono text-[10px] text-ink-dim underline-offset-2 transition-colors hover:text-brand hover:underline"
            >
              open session →
            </Link>
          )}
        </div>
        <div className="mt-1.5 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
          {rev.reason}
        </div>
      </div>

      {actionErr !== null && (
        <div
          role="alert"
          className="rounded-md border border-red/40 bg-red/10 px-2.5 py-1.5 font-mono text-[11px] text-red"
        >
          {actionErr}
        </div>
      )}

      {/* Apply 409 — the docs that drifted, each with the drift diff, and the
          one action that can unblock: reload the review against the live docs. */}
      {conflicts !== null && (
        <div className="rounded-lg border border-amber/40 bg-amber/5 px-3 py-2.5">
          <div className="font-mono text-[11px] font-semibold text-amber">
            content changed on disk since staging — apply refused, nothing was written
          </div>
          <ul className="mt-2 space-y-2">
            {conflicts.map((c) => (
              <li key={c.docPath}>
                <div className="font-mono text-[11px] text-ink">{c.docPath}</div>
                {c.diff !== '' && <DiffBlock diff={c.diff} />}
              </li>
            ))}
          </ul>
          <button
            type="button"
            onClick={load}
            className="mt-1 rounded-md border border-line-strong bg-surface2 px-2.5 py-1 font-mono text-[10.5px] text-ink transition-colors hover:bg-surface2/70"
          >
            Reload review
          </button>
        </div>
      )}

      {/* One collapsible block per file. */}
      <div className="space-y-2">
        {rev.files.map((f) => (
          <details
            key={f.docPath}
            open
            className="overflow-hidden rounded-lg border border-line bg-surface/40"
          >
            <summary className="flex cursor-pointer flex-wrap items-center gap-1.5 px-3 py-2 select-none hover:bg-surface2/50">
              <span className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${ACTION_CHIP[f.action]}`}>
                {f.action}
              </span>
              <span className="min-w-0 font-mono text-[11.5px] break-all text-ink">
                {f.action === 'rename' && f.renameFrom !== undefined
                  ? `${f.renameFrom} → ${f.docPath}`
                  : f.docPath}
              </span>
              {f.stale === true && (
                <span
                  className="rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[9.5px] text-amber"
                  data-tip="the live file changed since this revision was staged — reload before applying"
                >
                  stale
                </span>
              )}
            </summary>
            <div className="px-3 pb-2">
              {f.diff !== undefined && f.diff !== '' ? (
                <DiffBlock diff={f.diff} />
              ) : (
                <div className="py-1 font-mono text-[10.5px] text-ink-faint">no content change</div>
              )}
            </div>
          </details>
        ))}
      </div>

      {/* Decision footer — only while the revision is still open. */}
      {!decided && (
        <div className="flex flex-wrap items-center justify-end gap-2 border-t border-line pt-3">
          {anyStale && (
            <span className="mr-auto font-mono text-[10.5px] text-amber">
              a file changed since staging — reload before applying
            </span>
          )}
          <button
            type="button"
            onClick={load}
            disabled={busy}
            className="rounded-lg border border-line px-3 py-1.5 font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink disabled:opacity-50"
          >
            Reload
          </button>
          <button
            type="button"
            onClick={() => setRejectOpen(true)}
            disabled={busy}
            className="rounded-lg border border-red/40 bg-red/5 px-3.5 py-1.5 font-mono text-[11.5px] text-red transition-colors hover:bg-red/10 disabled:opacity-50"
          >
            Reject
          </button>
          <button
            type="button"
            onClick={() => setConfirmOpen(true)}
            disabled={busy || anyStale}
            data-tip={
              anyStale
                ? 'a file is stale — reload the review first'
                : 'write the staged changes into the plan docs'
            }
            className="rounded-lg border border-green/45 bg-green/12 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Apply revision
          </button>
        </div>
      )}
      {decided && (
        <div className="border-t border-line pt-3 font-mono text-[11px] text-ink-dim">
          {rev.status} {rev.decidedAt !== undefined ? `· ${fmtDateTime(rev.decidedAt)}` : ''}
          {rev.decidedBy !== undefined ? ` · by ${rev.decidedBy}` : ''}
        </div>
      )}

      {confirmOpen && (
        <DecisionDialog
          title="Apply this revision?"
          body={`${String(rev.files.length)} plan doc${rev.files.length === 1 ? '' : 's'} will change on disk: ${rev.files
            .map((f) => f.docPath)
            .join(', ')}. This is the one irreversible step.`}
          confirmLabel={busy ? 'applying…' : 'Apply'}
          confirmCls="border-green/45 bg-green/12 text-green hover:bg-green/20"
          busy={busy}
          onClose={() => setConfirmOpen(false)}
          onConfirm={() => apply()}
        />
      )}
      {rejectOpen && (
        <DecisionDialog
          title="Reject this revision?"
          body="No plan file changes. The note (optional) is recorded on the revision."
          confirmLabel={busy ? 'rejecting…' : 'Reject'}
          confirmCls="border-red/40 bg-red/10 text-red hover:bg-red/20"
          busy={busy}
          withNote
          onClose={() => setRejectOpen(false)}
          onConfirm={(note) => reject(note)}
        />
      )}
    </div>
  );
}

/** Confirm/reject dialog — the same focus-trap / Escape / focus-restore shape
 * as planning/RefineModal (WCAG 2.2 AA). Mounted only while open so refs and
 * effects start fresh every time. */
function DecisionDialog({
  title,
  body,
  confirmLabel,
  confirmCls,
  busy,
  withNote = false,
  onClose,
  onConfirm,
}: {
  title: string;
  body: string;
  confirmLabel: string;
  confirmCls: string;
  busy: boolean;
  /** Offer an optional note textarea (the reject path). */
  withNote?: boolean;
  onClose: () => void;
  onConfirm: (note: string) => void;
}): JSX.Element {
  const [note, setNote] = useState('');
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const primaryRef = useRef<HTMLButtonElement | null>(null);
  const noteRef = useRef<HTMLTextAreaElement | null>(null);
  // Remember the trigger so focus returns to it on close (WCAG 2.2 §2.4.3).
  const previouslyFocused = useRef<HTMLElement | null>(null);

  // Dirty = the note differs from the empty value it opens with.
  const discard = useDiscardGuard(withNote && note.trim() !== '', onClose, { disabled: busy });
  const { requestClose } = discard;

  useEffect(() => {
    previouslyFocused.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    (withNote ? noteRef.current : primaryRef.current)?.focus();
    return () => {
      previouslyFocused.current?.focus();
    };
  }, [withNote]);

  useEffect(() => {
    // Escape closes; Tab/Shift+Tab stays trapped inside the dialog.
    const FOCUSABLE =
      'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])';
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        // The discard confirm, when up, claims Esc in its own capture listener
        // before this one ever sees it.
        e.preventDefault();
        requestClose();
        return;
      }
      if (e.key !== 'Tab') return;
      const root = dialogRef.current;
      if (root === null) return;
      const focusable = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => el.offsetParent !== null,
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (first === undefined || last === undefined) return;
      const activeInRoot = root.contains(document.activeElement);
      if (e.shiftKey) {
        if (!activeInRoot || document.activeElement === first) {
          e.preventDefault();
          last.focus();
        }
      } else {
        if (!activeInRoot || document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [requestClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={title}
      onClick={requestClose}
    >
      <div
        ref={dialogRef}
        className="w-full max-w-md rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="font-display text-[14px] font-bold text-ink">{title}</div>
        <p className="mt-1 text-[12.5px] leading-relaxed text-ink-dim">{body}</p>
        {withNote && (
          <>
            <label
              className="mt-3 mb-1 block font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase"
              htmlFor="revision-reject-note"
            >
              note (optional)
            </label>
            <textarea
              ref={noteRef}
              id="revision-reject-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              rows={3}
              disabled={busy}
              placeholder="Why this proposal does not fit…"
              className="w-full resize-y rounded-lg border border-line bg-field px-2.5 py-2 text-[12.5px] leading-relaxed text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-brand/50 disabled:opacity-50"
            />
          </>
        )}
        <div className="mt-3.5 flex flex-wrap justify-end gap-2">
          <button
            type="button"
            onClick={requestClose}
            disabled={busy}
            className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            ref={primaryRef}
            type="button"
            onClick={() => onConfirm(note)}
            disabled={busy}
            className={`rounded-lg border px-3.5 py-1.5 font-mono text-[11.5px] font-semibold transition-colors disabled:opacity-50 ${confirmCls}`}
          >
            {confirmLabel}
          </button>
        </div>
      </div>

      <ConfirmDialog
        {...discard.confirmProps}
        title="Discard note?"
        confirmLabel="discard"
        danger
      >
        The note you typed will be lost.
      </ConfirmDialog>
    </div>
  );
}
