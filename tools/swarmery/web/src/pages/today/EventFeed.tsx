// The list column of Today (handoff §3): what happened, newest first, in Today and
// Yesterday. A row opens the entity it is about. No row is selected until `j`/`k`
// picks one, because the feed drives no detail: the detail beside it is the day.

import { useLingui } from '@lingui/react/macro';
import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ListGroup } from '../../components/place/ListGroup';
import { ListPane } from '../../components/place/ListPane';
import { ListRow } from '../../components/place/ListRow';
import { StateGlyph } from '../../components/place/StateGlyph';
import { compactSpan } from '../../lib/sessionsView';
import { stepKey } from '../../lib/useGlobalKeys';
import { filterFeed, groupFeed, type FeedEvent } from './feedModel';

function FeedRows({
  events,
  now,
  selected,
}: {
  events: readonly FeedEvent[];
  now: number;
  selected: string | null;
}): JSX.Element {
  return (
    <>
      {events.map((e) => (
        <ListRow
          key={e.key}
          // The feed is history, so a needs-you event does not pulse here.
          glyph={<StateGlyph state={e.state} className="animate-none!" />}
          title={e.title}
          summary={e.summary}
          age={compactSpan(now - Date.parse(e.at))}
          selected={e.key === selected}
          to={e.to}
        />
      ))}
    </>
  );
}

export function EventFeed({
  events,
  now,
  loading,
}: {
  events: readonly FeedEvent[];
  now: number;
  loading: boolean;
}): JSX.Element {
  const { t } = useLingui();
  const navigate = useNavigate();
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState<string | null>(null);
  const groups = useMemo(() => groupFeed(filterFeed(events, query), now), [events, query, now]);
  const rows = [...groups.today, ...groups.yesterday];
  const keys = rows.map((e) => e.key);

  const step = (dir: 1 | -1): void => setSelected(stepKey(keys, selected, dir) ?? null);
  const open = (): void => {
    const row = rows.find((e) => e.key === selected);
    if (row !== undefined) void navigate(row.to);
  };

  const emptyText =
    query !== ''
      ? t`No event matches.`
      : loading
        ? t`Loading…`
        : t`Nothing happened yet. Sessions, plan runs and decisions land here.`;

  return (
    <ListPane
      title={t`Today`}
      sub={t`what happened · newest first`}
      query={query}
      onQueryChange={setQuery}
      searchHint={t`filter events`}
      keys={{ onNext: () => step(1), onPrev: () => step(-1), onPrimary: open }}
      empty={
        rows.length === 0 ? (
          <p className="m-0 px-[14px] py-3 text-[12.5px] leading-[normal] text-ink-dim">{emptyText}</p>
        ) : undefined
      }
    >
      {groups.today.length > 0 && (
        <ListGroup label={t`Today`} count={groups.today.length}>
          <FeedRows events={groups.today} now={now} selected={selected} />
        </ListGroup>
      )}
      {groups.yesterday.length > 0 && (
        <ListGroup label={t`Yesterday`} count={groups.yesterday.length}>
          <FeedRows events={groups.yesterday} now={now} selected={selected} />
        </ListGroup>
      )}
    </ListPane>
  );
}
