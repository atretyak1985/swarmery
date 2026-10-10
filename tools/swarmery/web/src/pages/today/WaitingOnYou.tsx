// "Waiting on you" (Canvas v3 phase 4, artboard 1a): the first three Inbox
// items in the Inbox's own order (inboxModel.sortItems — urgent approvals
// first), each with one small action. A plain approval can be approved in
// place; everything else — and an approval that asks questions — opens its
// Inbox tab.

import { Trans, useLingui } from '@lingui/react/macro';
import { useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { resolveApproval } from '../../api';
import { questionsOf } from '../../lib/approvals';
import { ageLabel, expiresInLabel, INBOX_TABS, KIND_META, type InboxItem } from '../inbox/inboxModel';

export const WAITING_ROWS = 3;

const ACTION_CLS = 'shrink-0 rounded-md border px-[9px] py-0.5 font-mono text-[10.5px]';

export function SectionHead({ label, children }: { label: string; children?: ReactNode }): JSX.Element {
  return (
    <div className="flex items-baseline gap-2.5">
      <h2 className="m-0 font-mono text-[10px] font-normal tracking-[0.14em] text-ink-faint uppercase">{label}</h2>
      <span aria-hidden className="h-px flex-1 bg-line" />
      {children}
    </div>
  );
}

function tabFor(item: InboxItem): string {
  return INBOX_TABS.find((tab) => tab.kind === item.kind)?.id ?? 'all';
}

function Row({
  item,
  inboxHref,
  onResolved,
}: {
  item: InboxItem;
  inboxHref: string;
  onResolved: () => void;
}): JSX.Element {
  const [busy, setBusy] = useState(false);
  const age =
    item.kind === 'approval' && item.expiresIso !== undefined ? expiresInLabel(item.expiresIso) : ageLabel(item.ageIso);
  // A production deploy is confirmed only in the session's terminal (the
  // daemon refuses a remote approve), so it opens its Inbox tab instead.
  const approvable =
    item.kind === 'approval' && item.raw.riskClass !== 'prod-deploy' && questionsOf(item.raw) === null;

  const approve = (): void => {
    if (item.kind !== 'approval') return;
    setBusy(true);
    resolveApproval(item.raw.id, 'approve')
      .catch(() => undefined)
      .finally(() => {
        setBusy(false);
        onResolved();
      });
  };

  return (
    <li
      data-testid="waiting-row"
      className="flex items-center gap-2.5 rounded-[10px] border border-line bg-surface px-3 py-2.5"
    >
      <span aria-hidden className={`size-[7px] shrink-0 rounded-full ${KIND_META[item.kind].dot}`} />
      <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{item.title}</span>
      {age !== '' && <span className="shrink-0 font-mono text-[10px] text-ink-faint">{age}</span>}
      {approvable ? (
        <button
          type="button"
          disabled={busy}
          onClick={approve}
          className={`${ACTION_CLS} border-green/45 font-bold text-green hover:bg-green/10 disabled:opacity-50`}
        >
          <Trans>approve</Trans>
        </button>
      ) : (
        <Link
          to={`${inboxHref}?tab=${tabFor(item)}`}
          className={`${ACTION_CLS} border-line-strong text-ink-3 hover:text-ink`}
        >
          <Trans>review</Trans>
        </Link>
      )}
    </li>
  );
}

export function WaitingOnYou({
  items,
  count,
  loading,
  slug,
  onResolved,
}: {
  items: readonly InboxItem[];
  count: number;
  loading: boolean;
  slug: string | null;
  onResolved: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const inboxHref = slug === null ? '/inbox' : `/p/${slug}/inbox`;
  const top = items.slice(0, WAITING_ROWS);
  return (
    <section data-testid="waiting-on-you" className="min-w-0">
      <SectionHead label={t`Waiting on you`}>
        <Link
          to={inboxHref}
          className={`font-mono text-[10.5px] hover:underline ${count > 0 ? 'text-amber' : 'text-ink-faint'}`}
        >
          <Trans>Inbox · {count} →</Trans>
        </Link>
      </SectionHead>
      {top.length === 0 ? (
        <p className="mt-2.5 rounded-[10px] border border-line bg-surface px-3 py-2.5 text-[12.5px] text-ink-dim">
          {loading ? t`Loading…` : t`Nothing waiting on you. Approvals, lessons and Advisor findings land here.`}
        </p>
      ) : (
        <ul className="m-0 mt-2.5 flex list-none flex-col gap-1.5 p-0">
          {top.map((item) => (
            <Row key={item.key} item={item} inboxHref={inboxHref} onResolved={onResolved} />
          ))}
        </ul>
      )}
    </section>
  );
}
