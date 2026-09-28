// Learning → The classifier (Canvas v3 phase 6, artboard 1e). Replaces the old
// /decisions page: every question reads as a sentence, the mode switch says
// off · watching · acting, and each card carries one status sentence. The label
// queue is NOT here — checking answers happens in the Inbox's classifier tab.

import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  type DecideMode,
  type DecisionsResponse,
  type QuestionStats,
  fetchDecisions,
  putDecisionMode,
} from '../../api/decisions';
import { UI_TERMS } from '../../lib/glossary';
import {
  CHECKS_FOR_TRUST,
  MODES,
  MODE_WORD,
  type StatusSegment,
  questionSentence,
  statusSentence,
} from './classifierModel';

const MARK: Record<NonNullable<StatusSegment['mark']>, string> = {
  amber: 'text-amber',
  red: 'text-red',
};

function ModeSwitch({
  question,
  value,
  busy,
  onChange,
}: {
  question: string;
  value: DecideMode;
  busy: boolean;
  onChange: (mode: DecideMode) => void;
}): JSX.Element {
  return (
    <fieldset
      aria-label={`mode for ${question}`}
      disabled={busy}
      className="ml-auto inline-flex shrink-0 gap-[2px] rounded-lg border border-line-strong bg-bg p-[2px] font-mono text-[10.5px]"
    >
      {MODES.map((m) => {
        const on = m === value;
        return (
          <button
            key={m}
            type="button"
            aria-pressed={on}
            onClick={() => {
              if (!on) onChange(m);
            }}
            className={`rounded-md px-2 py-[2px] focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand ${
              on ? 'bg-line text-ink' : 'text-ink-faint hover:text-ink'
            }`}
          >
            {MODE_WORD[m]}
          </button>
        );
      })}
    </fieldset>
  );
}

function QuestionCard({
  q,
  busy,
  inboxHref,
  onMode,
}: {
  q: QuestionStats;
  busy: boolean;
  inboxHref: string;
  onMode: (id: string, mode: DecideMode) => void;
}): JSX.Element {
  const s = statusSentence(q);
  const title = questionSentence(q.questionId);
  return (
    <li
      className={`rounded-xl px-[14px] py-3 ${
        s.empty ? 'border border-dashed border-line-strong' : 'border border-line bg-surface'
      }`}
    >
      <div className="flex flex-wrap items-baseline gap-[10px]">
        <span className={`text-[13.5px] font-medium ${s.empty ? 'text-ink-3' : 'text-ink'}`}>{title}</span>
        <span className="font-mono text-[10px] text-ink-faint">{q.questionId}</span>
        <ModeSwitch question={title} value={q.mode} busy={busy} onChange={(m) => onMode(q.questionId, m)} />
      </div>
      <p className={`mt-[5px] text-[12.5px] leading-normal ${s.empty ? 'text-ink-faint' : 'text-ink-3'}`}>
        {s.segments.map((seg, i) =>
          seg.mark === undefined ? (
            <span key={i}>{seg.text}</span>
          ) : (
            <span key={i} className={MARK[seg.mark]}>
              {seg.text}
            </span>
          ),
        )}
      </p>
      {!s.empty && (
        <Link to={inboxHref} className="mt-1 inline-block font-mono text-[10.5px] text-brand hover:underline">
          check in Inbox →
        </Link>
      )}
    </li>
  );
}

function ModesHelp(): JSX.Element {
  return (
    <aside aria-labelledby="modes-heading">
      <h2
        id="modes-heading"
        className="font-mono text-[10px] font-normal tracking-[0.14em] text-ink-faint uppercase"
      >
        How the modes work
      </h2>
      <dl className="mt-[10px] flex flex-col gap-2 text-[12px] leading-normal text-ink-3">
        <div className="flex gap-[10px]">
          <dt className="w-14 shrink-0 pt-[2px] font-mono text-[10.5px] font-semibold text-ink-faint">
            {MODE_WORD.off}
          </dt>
          <dd>Not asked. Nothing logged.</dd>
        </div>
        <div className="flex gap-[10px]">
          <dt className="w-14 shrink-0 pt-[2px] font-mono text-[10.5px] font-semibold text-ink">
            {MODE_WORD.shadow}
          </dt>
          <dd>
            Asked and logged; answers go to your Inbox for checking.{' '}
            <b className="font-medium text-ink-2">Nothing acts on them.</b>
          </dd>
        </div>
        <div className="flex gap-[10px]">
          <dt className="w-14 shrink-0 pt-[2px] font-mono text-[10.5px] font-semibold text-amber">
            {MODE_WORD.active}
          </dt>
          <dd>
            Answers above the question&apos;s confidence bar (60 % unless set otherwise) are used — as
            labels on sessions, and in D1 to decide what happens after a run.
          </dd>
        </div>
      </dl>
      <p className="mt-[14px] rounded-[10px] border border-amber/30 bg-amber/5 px-3 py-[10px] text-[11.5px] leading-normal text-ink-3">
        <b className="font-medium text-amber">Rule of thumb:</b> switch to {MODE_WORD.active} only when
        “{UI_TERMS.agreement.ui}” is above the bar for {String(CHECKS_FOR_TRUST)}+ checked sessions.
      </p>
    </aside>
  );
}

/** The classifier tab. `inboxHref` is the Inbox's classifier tab in this scope. */
export function Classifier({ inboxHref }: { inboxHref: string }): JSX.Element {
  const [data, setData] = useState<DecisionsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    fetchDecisions()
      .then(setData)
      .catch((e: unknown) => setErr(String(e)));
  }, []);

  const onMode = useCallback((id: string, mode: DecideMode) => {
    setBusy(true);
    putDecisionMode(id, mode)
      .then((d) => {
        setData(d);
        setErr(null);
      })
      .catch((e: unknown) => setErr(String(e)))
      .finally(() => setBusy(false));
  }, []);

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_260px]">
      <div>
        <p className="max-w-[60ch] text-[13px] leading-relaxed text-ink-dim">
          A small local model reads each finished session and answers a few fixed questions. It is{' '}
          <b className="font-medium text-ink-2">{MODE_WORD.shadow}</b> until you have checked enough of
          its answers to trust it; only then does <b className="font-medium text-ink-2">{MODE_WORD.active}</b>{' '}
          make sense.
        </p>
        {data !== null && !data.configured && (
          <section
            aria-labelledby="classifier-setup-title"
            className="mt-3 max-w-[60ch] rounded border border-line p-4 text-[12px]"
          >
            <h2 id="classifier-setup-title" className="text-ink">
              No local model configured — this is optional
            </h2>
            <p className="mt-1 text-ink-dim">
              Everything else in swarmery works without it. Until a model is set up, no question is
              asked and runs settle by the deterministic rules alone.
            </p>
            <ol className="mt-3 list-decimal space-y-1 pl-5 text-ink-dim">
              <li>
                Run an OpenAI-compatible model server: LM Studio (<code>localhost:1234</code>), Ollama (
                <code>localhost:11434/v1</code>), llama.cpp or vLLM. A 7–14B instruct model is enough.
              </li>
              <li>
                Set <code>SWARMERY_DECIDE_URL</code> and <code>SWARMERY_DECIDE_MODEL</code> in the
                daemon&apos;s environment.
              </li>
              <li>Restart the daemon. Every question starts in shadow: logged, never acted on.</li>
            </ol>
            <Link
              to="/docs/guide-decisions"
              className="mt-3 inline-block text-brand transition-opacity hover:opacity-80"
            >
              Setup guide →
            </Link>
          </section>
        )}
        {err !== null && (
          <div role="alert" className="mt-3 text-[12px] text-red">
            {err}
          </div>
        )}
        {data === null && err === null && <div className="mt-4 text-[12px] text-ink-dim">loading…</div>}
        {data !== null && (
          <ul className="mt-4 flex flex-col gap-2">
            {data.questions.map((q) => (
              <QuestionCard key={q.questionId} q={q} busy={busy} inboxHref={inboxHref} onMode={onMode} />
            ))}
          </ul>
        )}
      </div>
      <ModesHelp />
    </div>
  );
}
