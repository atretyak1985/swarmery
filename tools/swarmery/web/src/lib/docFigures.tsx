// Canonical illustrations for the docs surface, addressed by name from a
// ```figure <name> fence (see markdown.tsx's fence dispatch).
//
// Why a registry of React components rather than images: a figure has to
// re-theme with the app (five palettes x light/dark), stay crisp at any zoom,
// and be greppable when the thing it depicts changes. A PNG is none of those.
// Each figure is static JSX in the app's own Tailwind tokens — no props, no
// state, no data fetching — so it renders identically in every doc that names
// it, and a guide can reuse one without duplicating markup.
//
// Adding a figure: write the component, register it in FIGURES, and reference
// it as ```figure <key>. An unregistered name renders an inline notice rather
// than crashing the doc — a typo in a guide must not take the page down.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';

/* ----- shared primitives ----- */

/** A boxed node used by the flow figures. */
function Node({
  label,
  sub,
  tone = 'plain',
}: {
  label: string;
  sub?: string;
  tone?: 'plain' | 'brand' | 'green' | 'amber';
}): JSX.Element {
  const ring =
    tone === 'brand'
      ? 'border-brand/50 bg-brand/8'
      : tone === 'green'
        ? 'border-green/50 bg-green/8'
        : tone === 'amber'
          ? 'border-amber/50 bg-amber/8'
          : 'border-line-strong bg-surface2';
  return (
    <div className={`rounded-lg border px-2.5 py-1.5 text-center ${ring}`}>
      <div className="text-[12px] leading-tight font-medium text-ink">{label}</div>
      {sub !== undefined && (
        <div className="mt-0.5 font-mono text-[9.5px] leading-tight text-ink-faint">{sub}</div>
      )}
    </div>
  );
}

/** The arrow between two flow nodes, with an optional edge label. */
function Arrow({ label }: { label?: string }): JSX.Element {
  return (
    <div className="flex shrink-0 flex-col items-center justify-center px-1">
      <div className="text-[13px] leading-none text-ink-faint">→</div>
      {label !== undefined && (
        <div className="mt-0.5 font-mono text-[9px] leading-none text-ink-faint">{label}</div>
      )}
    </div>
  );
}

/** The outer frame every figure shares.
 *
 * role="img" sits on an inner wrapper around the drawing ONLY, never on the
 * <figure>: the role flattens its whole subtree into a single leaf in the
 * accessibility tree, so putting it on the figure would swallow the
 * <figcaption> — a screen reader would get `label` and the caption text would
 * simply vanish. Scoped this way, AT reads the drawing as one labelled image
 * and then the caption as its caption, which is what a sighted reader gets. */
function Figure({
  caption,
  label,
  note,
  children,
}: {
  caption: string;
  label: string;
  /** Prose that belongs to the figure but must stay READABLE — it is rendered
   * outside the role="img" scope, which would otherwise flatten it away. */
  note?: React.ReactNode;
  children: React.ReactNode;
}): JSX.Element {
  return (
    <figure className="my-3 overflow-x-auto rounded-lg border border-line bg-surface p-3 first:mt-0 last:mb-0">
      <div role="img" aria-label={label}>
        {children}
      </div>
      {note !== undefined && (
        <div className="mt-2.5 space-y-1 border-t border-line-soft pt-2 text-[11px] leading-snug text-ink-dim">
          {note}
        </div>
      )}
      <figcaption className="mt-2.5 border-t border-line-soft pt-2 font-mono text-[10px] tracking-[0.04em] text-ink-faint uppercase">
        {caption}
      </figcaption>
    </figure>
  );
}

/* ----- board-lanes ----- */

function Chip({ text, tone = 'plain' }: { text: string; tone?: 'plain' | 'act' | 'good' | 'bad' }): JSX.Element {
  const cls =
    tone === 'act'
      ? 'border-brand/40 bg-brand/10 text-brand'
      : tone === 'good'
        ? 'border-green/40 bg-green/10 text-green'
        : tone === 'bad'
          ? 'border-red/40 bg-red/10 text-red'
          : 'border-line-strong bg-surface text-ink-dim';
  return (
    <span className={`rounded border px-1.5 py-px font-mono text-[9.5px] whitespace-nowrap ${cls}`}>
      {text}
    </span>
  );
}

function MiniCard({
  id,
  title,
  chips,
}: {
  id: string;
  title: string;
  chips: { text: string; tone?: 'plain' | 'act' | 'good' | 'bad' }[];
}): JSX.Element {
  return (
    <div className="rounded-lg border border-line-strong bg-surface px-2.5 py-2">
      <div className="font-mono text-[9.5px] text-ink-faint">{id}</div>
      <div className="mt-1 text-[12px] leading-snug text-ink">{title}</div>
      <div className="mt-1.5 flex flex-wrap gap-1">
        {chips.map((c, i) => (
          <Chip key={i} text={c.text} {...(c.tone === undefined ? {} : { tone: c.tone })} />
        ))}
      </div>
    </div>
  );
}

function Lane({
  title,
  sub,
  children,
}: {
  title: string;
  sub: string;
  children: React.ReactNode;
}): JSX.Element {
  return (
    <div className="min-w-[180px] flex-1 rounded-lg border border-line bg-surface2/50 p-2">
      <div className="mb-2 flex items-baseline justify-between gap-2 border-b border-line-soft pb-1.5">
        <b className="text-[12.5px] font-semibold text-ink">{title}</b>
        <span className="font-mono text-[9.5px] text-ink-faint">{sub}</span>
      </div>
      <div className="space-y-2">{children}</div>
    </div>
  );
}

function GroupHead({ text }: { text: string }): JSX.Element {
  return (
    <div className="pt-0.5 font-mono text-[9.5px] tracking-[0.06em] text-ink-faint uppercase">
      {text}
    </div>
  );
}

function BoardLanesFigure(): JSX.Element {
  const { t } = useLingui();
  return (
    <Figure caption={t`The board's three lanes`} label={t`Mockup of the board's three lanes`}>
      <div className="flex flex-wrap gap-2">
        <Lane title={t`Inbox`} sub="triage">
          <MiniCard
            id="T-9k2f1a · session"
            title={t`Remove the dead flag in settings`}
            chips={[
              { text: t`▶ Run`, tone: 'act' },
              { text: t`✎ Plan`, tone: 'act' },
              { text: t`✕ Dismiss` },
            ]}
          />
          <MiniCard
            id="T-4mq88x · llm"
            title={t`Add an index on sessions.cwd`}
            chips={[{ text: t`idle 9d · TTL 14d` }]}
          />
        </Lane>
        <Lane title={t`Working`} sub="todo + in_progress">
          <GroupHead text={t`Queued — waiting for a slot`} />
          <MiniCard
            id="T-7pp3vd · plan-first"
            title={t`Migrate routine config to v2`}
            chips={[{ text: t`prio high` }, { text: t`↩ Inbox` }, { text: t`❙❙ Pause` }]}
          />
          <GroupHead text={t`Running`} />
          <MiniCard
            id="T-3mg7xy · standard"
            title={t`Fix the flaky worker test`}
            chips={[{ text: 'swarm/T-3mg7xy', tone: 'act' }, { text: t`⌸ Terminal` }]}
          />
        </Lane>
        <Lane title={t`Review`} sub="in_review">
          <MiniCard
            id="T-5rr0bn · review-heavy"
            title={t`Retries for the BLE handshake`}
            chips={[
              { text: t`verify: pass`, tone: 'good' },
              { text: t`⇧ Land`, tone: 'act' },
              { text: t`↻ Re-run`, tone: 'act' },
              { text: t`🗑 Discard`, tone: 'bad' },
            ]}
          />
        </Lane>
      </div>
      <div className="mt-2 flex items-center justify-between rounded-lg border border-line-soft bg-surface2/40 px-2.5 py-1.5 font-mono text-[10px] text-ink-faint">
        <span>
          <Trans>▸ History</Trans>
        </span>
        <span>
          <Trans>2 done · 237 archived</Trans>
        </span>
      </div>
    </Figure>
  );
}

/* ----- card-lifecycle ----- */

function CardLifecycleFigure(): JSX.Element {
  const { t } = useLingui();
  return (
    <Figure
      caption={t`A card's life: capture → queue → run → review → land`}
      label={t`Flow of a board card from capture to landing`}
      note={
        <>
          <div>
            <Trans>
              <span className="font-mono text-[10px] text-ink-faint">branch ·</span> Dismiss, or a 14-day TTL in
              triage, archives a captured card instead.
            </Trans>
          </div>
          <div>
            <Trans>
              <span className="font-mono text-[10px] text-ink-faint">branch ·</span> From Review, Re-run and a
              failed verify both send the card back to Queued; Discard archives it and deletes the branch.
            </Trans>
          </div>
        </>
      }
    >
      <div className="flex min-w-max flex-wrap items-center gap-1">
        {/* i18n-ignore: the sub line lists origin codes (session, llm, manual) */}
        <Node label={t`Captured`} sub="session · llm · manual" />
        <Arrow label={t`Run`} />
        <Node label={t`Queued`} sub="todo" />
        <Arrow label={t`9 gates`} />
        <Node label={t`Running`} sub="swarm/T-id" tone="brand" />
        <Arrow label={t`playbook end`} />
        <Node label={t`Review`} sub="in_review" tone="amber" />
        <Arrow label={t`Land`} />
        <Node label={t`Done`} sub="push + PR" tone="green" />
      </div>
    </Figure>
  );
}

/* ----- dispatch-gates ----- */

/** The dispatcher's admission gates, in the order it applies them.
 *
 * SOURCE OF TRUTH: `Service.Schedule` in internal/dispatch/service.go. This array
 * is a hand-written English restatement of that sequence — nothing enforces the
 * match, so reordering, adding or removing a gate there means editing this list
 * (and docs/guides/guide-plans.md, which narrates the same nine). */
const GATES: MessageDescriptor[] = [
  msg`The global dispatch switch, and the dispatcher pause`,
  msg`Projects on the locked-down preset — refused, stamped into dispatchError`,
  msg`A pause on this specific project`,
  msg`The concurrent-run limit (default 2)`,
  msg`Single-flight: the same card never starts twice`,
  msg`Single-flight per worktree: a fix card shares its root card’s tree`,
  msg`The worktree limit (default 4)`,
  msg`Dependencies: each must be done or archived, and none may carry a fail verdict`,
  msg`File-scope overlap with the project’s other live cards (an empty scope conflicts with everything)`,
];

function DispatchGatesFigure(): JSX.Element {
  const { t, i18n } = useLingui();
  return (
    <Figure
      caption={t`Admission gates, in the order the dispatcher applies them`}
      label={t`The nine dispatcher admission gates in order`}
      note={
        <div>
          <Trans>
            Clear all nine and the card gets an isolated worktree on{' '}
            <span className="font-mono text-[10.5px] text-ink-3">swarm/&lt;T-id&gt;</span>, cut from the tip of
            whatever branch the main checkout is on — that SHA is persisted, so verification and the review
            diff both stand on it.
          </Trans>
        </div>
      }
    >
      <ol className="space-y-1">
        {GATES.map((gate, i) => (
          <li key={i} className="flex items-start gap-2.5">
            <span className="mt-px shrink-0 rounded border border-line-strong bg-surface2 px-1.5 py-px font-mono text-[10px] tabular-nums text-brand">
              {String(i + 1).padStart(2, '0')}
            </span>
            <span className="text-[12px] leading-snug text-ink-2">{i18n._(gate)}</span>
          </li>
        ))}
      </ol>
    </Figure>
  );
}

/* ----- plan-dag ----- */

function PhaseBox({
  n,
  title,
  dep,
}: {
  n: string;
  title: string;
  dep: string;
}): JSX.Element {
  return (
    <div className="min-w-[150px] flex-1 rounded-lg border border-line-strong bg-surface2 px-2.5 py-2">
      <div className="font-mono text-[9.5px] text-brand">
        <Trans>phase {n}</Trans>
      </div>
      <div className="mt-0.5 text-[12px] leading-snug text-ink">{title}</div>
      <div className="mt-1 font-mono text-[9.5px] text-ink-faint">
        <Trans>depends on: {dep}</Trans>
      </div>
    </div>
  );
}

function PlanDagFigure(): JSX.Element {
  const { t } = useLingui();
  return (
    <Figure
      caption={t`A plan is a README plus a phase DAG`}
      label={t`Plan structure: README over a graph of phase documents`}
      note={
        <div>
          <Trans>
            Phases with no edge between them run in parallel; each phase document carries its own copy-paste
            agent prompt and acceptance criteria, so it is executable standalone.
          </Trans>
        </div>
      }
    >
      <div className="rounded-lg border border-brand/40 bg-brand/8 px-2.5 py-2">
        {/* i18n-ignore: a file name */}
        <div className="text-[12px] font-medium text-ink">README.md</div>
        <div className="mt-0.5 font-mono text-[9.5px] text-ink-faint">
          <Trans>objective · sequencing table · risks · definition of done</Trans>
        </div>
      </div>
      <div className="py-1 text-center text-[13px] leading-none text-ink-faint">↓</div>
      <div className="flex flex-wrap gap-2">
        <PhaseBox n="1" title={t`Schema + migration`} dep="—" />
        <PhaseBox n="2" title={t`API endpoints`} dep="—" />
      </div>
      <div className="py-1 text-center text-[13px] leading-none text-ink-faint">↓</div>
      <div className="flex flex-wrap gap-2">
        <PhaseBox n="3" title={t`UI screen`} dep="1, 2" />
        <PhaseBox n="4" title={t`Docs + rollout`} dep="1, 2" />
      </div>
    </Figure>
  );
}

/* ----- registry ----- */

const FIGURES: Record<string, () => JSX.Element> = {
  'board-lanes': BoardLanesFigure,
  'card-lifecycle': CardLifecycleFigure,
  'dispatch-gates': DispatchGatesFigure,
  'plan-dag': PlanDagFigure,
};

/** The names a ```figure fence may use. Exported so a test can assert the
 * registry and the guides agree. */
export const FIGURE_NAMES: string[] = Object.keys(FIGURES);

/** Render the figure a ```figure <name> fence asked for.
 *
 * An unknown name renders a visible inline notice rather than throwing: a
 * typo in one guide must degrade to a legible "this figure is missing" mark,
 * not blank the whole doc. */
export function DocFigure({ name }: { name: string }): JSX.Element {
  const F = FIGURES[name];
  if (F === undefined) {
    return (
      <div className="my-2 rounded border border-amber/40 bg-amber/8 px-3 py-2 font-mono text-[11px] text-ink-dim">
        <Trans>unknown figure: {name}</Trans>
      </div>
    );
  }
  return <F />;
}
