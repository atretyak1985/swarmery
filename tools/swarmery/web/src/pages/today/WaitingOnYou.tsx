// "Waiting on you" (handoff §3): the first four blockers of the needs-you queue,
// in that page's order. A row opens what it blocks on: the session, whose gate card
// decides it, or the phase. A plain permission can still be approved in place, as
// the old home did: the action shows over the age on hover or keyboard focus, so at
// rest the row is the design's one link.

import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { resolveApproval } from '../../api';
import type { NeedsYouItem } from '../../api/types';
import { StateGlyph } from '../../components/place/StateGlyph';
import { groupItems, itemGlyph, itemKey } from '../../lib/needsYou';
import { compactSpan } from '../../lib/sessionsView';
import { blockerHref, type FeedLinks } from './feedModel';
import { PanelEmpty, PanelList, PanelRow, SectionHead, SectionLink } from './panel';

export const WAITING_ROWS = 4;

function ApproveButton({ requestId, onResolved }: { requestId: number; onResolved: () => void }): JSX.Element {
  const [busy, setBusy] = useState(false);
  const approve = (): void => {
    setBusy(true);
    resolveApproval(requestId, 'approve')
      .catch(() => undefined)
      .finally(() => {
        setBusy(false);
        onResolved();
      });
  };
  return (
    <button
      type="button"
      disabled={busy}
      onClick={approve}
      className="rounded-[6px] border border-green bg-surface px-[9px] py-0.5 font-mono text-[10.5px] font-bold text-green disabled:opacity-50"
    >
      <Trans>approve</Trans>
    </button>
  );
}

function Row({
  item,
  links,
  now,
  onResolved,
}: {
  item: NeedsYouItem;
  links: FeedLinks;
  now: number;
  onResolved: () => void;
}): JSX.Element {
  const requestId = item.kind === 'approval' ? item.requestId : null;
  return (
    <PanelRow
      testId="waiting-row"
      to={blockerHref(item, links)}
      glyph={<StateGlyph state={itemGlyph(item.kind)} size={11} className="animate-none!" />}
      title={item.phase?.name ?? item.sessionName}
      summary={item.preview !== '' ? item.preview : item.question}
      age={compactSpan(now - Date.parse(item.blockingSince))}
      action={requestId === null ? undefined : <ApproveButton requestId={requestId} onResolved={onResolved} />}
    />
  );
}

export function WaitingOnYou({
  items,
  count,
  loading,
  links,
  now,
  onResolved,
}: {
  items: readonly NeedsYouItem[];
  count: number;
  loading: boolean;
  links: FeedLinks;
  now: number;
  onResolved: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const top = groupItems(items)
    .flatMap((g) => g.items)
    .slice(0, WAITING_ROWS);
  return (
    <section data-testid="waiting-on-you" className="min-w-0">
      <SectionHead label={t`Waiting on you`} tone="brand">
        <SectionLink to={links.needsYou}>
          <Trans>Needs you · {count} →</Trans>
        </SectionLink>
      </SectionHead>
      <div className="mt-2">
        {top.length === 0 ? (
          <PanelEmpty>{loading ? t`Loading…` : t`Nothing waits on you.`}</PanelEmpty>
        ) : (
          <PanelList>
            {top.map((item) => (
              <Row key={itemKey(item)} item={item} links={links} now={now} onResolved={onResolved} />
            ))}
          </PanelList>
        )}
      </div>
    </section>
  );
}
