// Swarm panel (mods-swarmery-link phase 6): what the project's sessions said to
// each other, who holds which file right now, and — with a sender — a form that
// relays a message from this session through its link mod.
//
//   feed    GET /api/swarm/messages  who → whom, body, time, delivery state
//   claims  GET /api/swarm/claims    live claims; a path two sessions hold is
//                                    a conflict (advisory — nothing is blocked)
//   send    POST /api/sessions/{id}/link/message {to, text}; {id} is the SENDER.
//           The command's progress arrives on link_command frames (kind
//           'message'); the row's delivered/reason on swarm_message frames.
//
// Mounted on SessionDetail (with a sender) and on the project overview
// (without: feed + claims only).

import { useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { fetchSessions, sendSwarmMessage } from '../../api';
import type {
  LinkCommandNote,
  LinkCommandState,
  Session,
  SwarmClaim,
  SwarmMessage,
  SwarmSessionRef,
  WSMessage,
} from '../../api/types';
import { Empty, ErrorBox, Loading } from '../../components/ui';
import { fmtAgo } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { useSwarmClaims, useSwarmMessages } from '../../lib/swarm';
import { useLiveUpdates } from '../../lib/ws';

/** The server's text limit, in characters (link.MaxMessageBody). */
export const SWARM_MESSAGE_MAX = 4096;

const BTN =
  'rounded border px-2 py-0.5 font-mono text-[10.5px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50';

/** Rune count, like the server's utf8.RuneCountInString. */
const runeCount = (s: string): number => Array.from(s).length;

const shortUuid = (uuid: string): string => uuid.slice(0, 8);

type SessionLabels = ReadonlyMap<number, string>;

// --- feed ------------------------------------------------------------------

function SessionLink({ ref_, labels }: { ref_: SwarmSessionRef | null; labels: SessionLabels }): JSX.Element | null {
  const href = useSessionHref();
  if (ref_ === null) return null;
  const name = ref_.title ?? labels.get(ref_.id) ?? shortUuid(ref_.uuid);
  return (
    <Link to={href(ref_.id)} className="min-w-0 truncate text-ink hover:text-brand hover:underline">
      {name}
    </Link>
  );
}

function Delivery({ m }: { m: SwarmMessage }): JSX.Element | null {
  const { t } = useLingui();
  // A receive row is the other end of a send: delivery is the send row's fact.
  if (m.kind === 'receive') return null;
  if (m.delivered === true) {
    return (
      <span data-testid="delivery" className="text-green">
        {t`✓ delivered`}
      </span>
    );
  }
  if (m.delivered === false) {
    const reason = m.reason ?? '';
    return (
      <span data-testid="delivery" className="text-red">
        {reason === '' ? t`not delivered` : t`not delivered: ${reason}`}
      </span>
    );
  }
  return (
    <span data-testid="delivery" className="text-ink-faint">
      {t`pending`}
    </span>
  );
}

function MessageRow({ m, labels }: { m: SwarmMessage; labels: SessionLabels }): JSX.Element {
  const { t } = useLingui();
  const kind = m.kind === 'send' ? t`sent` : m.kind === 'receive' ? t`received` : t`dashboard`;
  const toAddress = m.toAddress ?? '?';
  return (
    <li className="flex flex-col gap-1 border-b border-line px-3 py-2 last:border-b-0" data-testid="swarm-message" data-kind={m.kind}>
      <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 font-mono text-[11px] text-ink-dim">
        <span className="rounded border border-line px-1 text-[9.5px] tracking-[0.06em] text-ink-faint uppercase">{kind}</span>
        {m.fromSession === null ? <span>{t`dashboard`}</span> : <SessionLink ref_={m.fromSession} labels={labels} />}
        <span aria-hidden="true">→</span>
        <span className="sr-only">{t`to`}</span>
        {m.toSession === null ? <span className="truncate">{toAddress}</span> : <SessionLink ref_={m.toSession} labels={labels} />}
        <span className="ml-auto shrink-0 text-ink-faint">{fmtAgo(m.at)}</span>
        <Delivery m={m} />
      </div>
      <p className="break-words whitespace-pre-wrap text-[12.5px] text-ink">
        {m.body}
        {m.truncated && (
          <span className="ml-1 font-mono text-[10.5px] text-ink-faint" data-testid="truncated">
            {t`… (truncated)`}
          </span>
        )}
      </p>
    </li>
  );
}

function Feed({ projectId, labels }: { projectId: number; labels: SessionLabels }): JSX.Element {
  const { t } = useLingui();
  const { data, error, reload } = useSwarmMessages(projectId);
  if (error !== null && data === null) return <ErrorBox message={error} onRetry={reload} />;
  if (data === null) return <Loading label={t`messages…`} />;
  if (data.length === 0) return <Empty>{t`No messages between this project's sessions yet.`}</Empty>;
  return (
    <ul className="rounded-lg border border-line bg-surface" aria-label={t`Swarm messages`}>
      {data.map((m) => (
        <MessageRow key={m.id} m={m} labels={labels} />
      ))}
    </ul>
  );
}

// --- claims ----------------------------------------------------------------

function Claims({ projectId, labels }: { projectId: number; labels: SessionLabels }): JSX.Element {
  const { t } = useLingui();
  const href = useSessionHref();
  const { data, error, reload } = useSwarmClaims(projectId);
  if (error !== null && data === null) return <ErrorBox message={error} onRetry={reload} />;
  if (data === null) return <Loading label={t`claims…`} />;
  if (data.claims.length === 0) return <Empty>{t`No files are claimed right now.`}</Empty>;

  const holders = new Map<string, SwarmClaim[]>();
  for (const c of data.claims) holders.set(c.path, [...(holders.get(c.path) ?? []), c]);
  const conflicted = new Set(data.conflicts.map((c) => c.path));

  return (
    <ul className="rounded-lg border border-line bg-surface" aria-label={t`File claims`}>
      {[...holders].map(([path, claims]) => {
        const clash = conflicted.has(path);
        return (
          <li
            key={path}
            data-testid="swarm-claim"
            data-conflict={clash ? 'true' : 'false'}
            className={`flex flex-wrap items-baseline gap-x-2 gap-y-0.5 border-b border-line px-3 py-2 font-mono text-[11px] last:border-b-0 ${clash ? 'bg-amber/10' : ''}`}
          >
            {clash && (
              <span className="rounded border border-amber/40 px-1 text-[9.5px] tracking-[0.06em] text-amber uppercase">
                {t`conflict`}
              </span>
            )}
            <span className="min-w-0 break-all text-ink">{path}</span>
            <span className="text-ink-faint">{t`held by`}</span>
            {claims.map((c) => (
              <Link key={c.sessionId} to={href(c.sessionId)} className="text-ink-dim hover:text-brand hover:underline">
                {labels.get(c.sessionId) ?? shortUuid(c.sessionUuid)}
              </Link>
            ))}
          </li>
        );
      })}
    </ul>
  );
}

// --- send form -------------------------------------------------------------

/** The command this form last sent. 'sending' until the POST answers. */
interface Sent {
  commandId: number | null;
  state: LinkCommandState | 'sending' | 'rejected';
  message?: string | undefined;
}

type Sender = Pick<Session, 'id' | 'link'>;

function SendForm({ sender, recipients }: { sender: Sender; recipients: readonly Session[] }): JSX.Element {
  const { t } = useLingui();
  const reasonId = useId();
  const [to, setTo] = useState('');
  const [text, setText] = useState('');
  const [sent, setSent] = useState<Sent | null>(null);
  // A frame can beat the 202 (a waiting mod polls at once): keep it by id.
  const early = useRef(new Map<number, LinkCommandNote>());

  const link = sender.link ?? null;
  const controllable = link?.controllable === true;
  const reason =
    link === null
      ? t`No swarmery link mod reports on this session, so it cannot be steered from the dashboard.`
      : t`The session's link mod is not polling for commands. Enable the swarmery-link "control" option and keep the session open.`;

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type !== 'link_command') return;
      const n = msg.payload;
      if (n.sessionId !== sender.id || n.kind !== 'message') return;
      early.current.set(n.commandId, n);
      setSent((prev) =>
        prev !== null && prev.commandId === n.commandId ? { ...prev, state: n.state, message: n.message } : prev,
      );
    },
    [sender.id],
  );
  useLiveUpdates(onMessage, () => undefined);

  const count = runeCount(text);
  const over = count > SWARM_MESSAGE_MAX;
  const busy = sent !== null && (sent.state === 'sending' || sent.state === 'queued' || sent.state === 'delivered');
  const canSend = controllable && to !== '' && text.trim() !== '' && !over && !busy;

  const send = async (): Promise<void> => {
    if (!canSend) return;
    setSent({ commandId: null, state: 'sending' });
    try {
      const { commandId } = await sendSwarmMessage(sender.id, Number(to), text);
      const seen = early.current.get(commandId);
      early.current.delete(commandId);
      setSent({ commandId, state: seen?.state ?? 'queued', message: seen?.message });
      setText('');
    } catch (err) {
      setSent({ commandId: null, state: 'rejected', message: err instanceof Error ? err.message : String(err) });
    }
  };

  const stateLabel = (s: Sent): string => {
    const msg = s.message ?? '';
    switch (s.state) {
      case 'sending':
        return t`sending…`;
      case 'rejected':
        return t`not sent: ${msg}`;
      case 'queued':
        return t`queued, waiting for the mod to pick it up`;
      case 'delivered':
        return t`handed to the session's mod`;
      case 'done':
        return t`sent`;
      case 'failed':
        return msg === '' ? t`failed` : t`failed: ${msg}`;
      case 'expired':
        return t`expired — the mod did not pick it up in time`;
    }
  };
  const tone =
    sent === null
      ? ''
      : sent.state === 'done'
        ? 'text-green'
        : sent.state === 'failed' || sent.state === 'rejected'
          ? 'text-red'
          : sent.state === 'expired'
            ? 'text-amber'
            : 'text-ink-dim';

  const toId = `${reasonId}-to`;
  const textId = `${reasonId}-text`;

  return (
    <form
      className="flex max-w-[640px] flex-col gap-2 rounded-lg border border-line bg-surface p-3"
      data-testid="swarm-send"
      onSubmit={(e) => {
        e.preventDefault();
        void send();
      }}
    >
      <label htmlFor={toId} className="font-mono text-[10.5px] text-ink-dim">
        {t`Recipient`}
      </label>
      <select
        id={toId}
        value={to}
        onChange={(e) => setTo(e.target.value)}
        disabled={!controllable}
        aria-describedby={controllable ? undefined : reasonId}
        className="rounded border border-line bg-field px-2 py-1 font-mono text-[11.5px] text-ink disabled:opacity-50"
      >
        <option value="">{t`Choose a session…`}</option>
        {recipients.map((r) => (
          <option key={r.id} value={String(r.id)}>
            {r.title ?? shortUuid(r.sessionUuid)}
          </option>
        ))}
      </select>
      {recipients.length === 0 && (
        <span className="font-mono text-[10.5px] text-ink-faint">
          {t`No other session of this project has a link mod to message.`}
        </span>
      )}
      <label htmlFor={textId} className="font-mono text-[10.5px] text-ink-dim">
        {t`Message`}
      </label>
      <textarea
        id={textId}
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={3}
        disabled={!controllable}
        aria-describedby={controllable ? undefined : reasonId}
        placeholder={t`What should the other session know?`}
        className="w-full resize-y rounded border border-line bg-field px-2 py-1 font-mono text-[11.5px] text-ink disabled:opacity-50"
      />
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="submit"
          disabled={!canSend}
          aria-describedby={controllable ? undefined : reasonId}
          className={`${BTN} border-brand/50 bg-brand/10 text-brand hover:bg-brand/20`}
        >
          {t`Send`}
        </button>
        <span
          data-testid="swarm-count"
          className={`font-mono text-[10.5px] ${over ? 'text-red' : 'text-ink-faint'}`}
        >
          {count} / {SWARM_MESSAGE_MAX}
        </span>
        <span id={reasonId} className={controllable ? 'sr-only' : 'font-mono text-[10.5px] text-ink-faint'}>
          {controllable ? '' : reason}
        </span>
      </div>
      {sent !== null && (
        <span role="status" data-testid="swarm-command-state" className={`font-mono text-[10.5px] ${tone}`}>
          {stateLabel(sent)}
        </span>
      )}
    </form>
  );
}

// --- panel -----------------------------------------------------------------

function Heading({ children }: { children: string }): JSX.Element {
  return <h2 className="mt-5 mb-2 font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">{children}</h2>;
}

export function SwarmPanel({
  projectId,
  projectSlug,
  sender,
  className = '',
}: {
  projectId: number;
  /** The project's DB slug — scopes the session list for names and recipients. */
  projectSlug: string;
  /** The session whose mod relays a typed message; omit for a read-only panel. */
  sender?: Sender | undefined;
  className?: string;
}): JSX.Element {
  const { t } = useLingui();
  const [sessions, setSessions] = useState<readonly Session[]>([]);
  // Names for claim holders and untitled message ends; best-effort, so a
  // failed list costs the names and the recipient select, not the panel.
  useEffect(() => {
    let live = true;
    fetchSessions({ project: projectSlug }, { limit: 100 })
      .then((page) => {
        if (live) setSessions(page.sessions);
      })
      .catch(() => {
        if (live) setSessions([]);
      });
    return () => {
      live = false;
    };
  }, [projectSlug]);
  const labels = new Map<number, string>();
  for (const s of sessions) labels.set(s.id, s.title ?? shortUuid(s.sessionUuid));
  const recipients = sender === undefined ? [] : sessions.filter((s) => s.id !== sender.id && s.link != null);

  return (
    <section aria-label={t`Swarm`} className={className} data-testid="swarm-panel">
      {sender !== undefined && (
        <>
          <Heading>{t`Send a message from this session`}</Heading>
          <SendForm sender={sender} recipients={recipients} />
        </>
      )}
      <Heading>{t`File claims`}</Heading>
      <Claims projectId={projectId} labels={labels} />
      <Heading>{t`Messages`}</Heading>
      <Feed projectId={projectId} labels={labels} />
    </section>
  );
}
