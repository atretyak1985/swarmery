// The brief: what this task is and where it came from (board redesign v2,
// phase 2). Always visible, first thing in the modal, and the only panel a card
// that has never run shows anything but its actions.
//
// It answers the two questions the old modal made a reader dig for. "What is
// this" was a `prompt` textarea 14 rows tall sitting under seven dispatcher
// knobs; here the prompt IS the brief — the sentence someone wrote about what
// needs doing — and it autosizes to its content up to six rows instead of
// reserving fourteen for a two-line card. "Where did this come from" had no
// answer at all beyond a "source session →" link at the very bottom; the source
// block now carries the captured quote, the link to the turn it was minted from,
// and the files that session had touched (0066 provenance).
//
// Labels are read-only here, as they already were: they are edited in
// NewTaskModal, and phase 2 is not the place to move that.

import { useEffect, useRef } from 'react';
import { Link } from 'react-router-dom';
import type { BoardTask } from '../../api/types';
import { useSessionHref } from '../../lib/sessionHref';
import { labelColor, sourceLine } from '../boardModel';
import { FieldLabel } from '../TaskFields';
import type { DraftSetter, TaskDraft } from './useTaskDraft';

/**
 * How tall the prompt grows before it starts scrolling. Six rows is the size at
 * which a one-paragraph task is fully visible and a pasted spec stops pushing
 * everything below it off the screen — the no-scroll budget for a card that
 * never ran is spent here or nowhere.
 */
export const PROMPT_MAX_ROWS = 6;

/** Vertical padding of the prompt box (py-1.5 twice), added to the row budget. */
const PROMPT_PADDING_PX = 12;

/**
 * Grows a textarea to fit its content, capped at `PROMPT_MAX_ROWS`, and turns
 * on its own scrollbar past that point.
 *
 * Measured from the element's OWN computed line-height rather than a hard-coded
 * pixel cap, so the cap stays six rows if the type scale ever moves. jsdom
 * reports no line-height and a zero scrollHeight, where this degrades to leaving
 * the element at its `rows` floor — the tests below assert the floor, not the
 * grown height, because the grown height is a browser measurement.
 */
function useAutosize(ref: React.RefObject<HTMLTextAreaElement | null>, value: string): void {
  useEffect(() => {
    const el = ref.current;
    if (el === null) return;
    el.style.height = 'auto';
    const line = Number.parseFloat(window.getComputedStyle(el).lineHeight);
    if (Number.isNaN(line) || el.scrollHeight === 0) return;
    const max = line * PROMPT_MAX_ROWS + PROMPT_PADDING_PX;
    el.style.height = `${String(Math.min(el.scrollHeight, max))}px`;
    el.style.overflowY = el.scrollHeight > max ? 'auto' : 'hidden';
  }, [ref, value]);
}

/**
 * Where the card came from, as a block rather than the card's one line: the same
 * `sourceLine` selector the card renders (phase 1), plus the two things a card
 * has no room for — the captured opening prompt and the files that session had
 * already touched.
 *
 * Renders for EVERY card, including a hand-written one, because "added by hand"
 * is an answer. The old modal showed provenance only when there was a session to
 * link, so a manual card's origin was a blank where a fact should be.
 */
function SourceBlock({ task }: { task: BoardTask }): JSX.Element {
  const sessionHref = useSessionHref();
  const line = sourceLine(task);
  const href =
    line.target === null
      ? null
      : line.target.kind === 'session'
        ? sessionHref(line.target.sessionId)
        : `/p/${line.target.slug}/plans`;
  const quote = task.source?.quote ?? null;
  const files = task.source?.files ?? [];
  return (
    <div>
      <FieldLabel>source</FieldLabel>
      <div className="font-mono text-[11px] text-ink-2">
        {href === null ? (
          <span data-tip={line.tip}>{line.text}</span>
        ) : (
          <Link
            to={href}
            data-tip={line.tip}
            className="underline transition-colors hover:text-ink"
          >
            {line.text}
          </Link>
        )}
      </div>
      {quote !== null && quote !== '' && (
        <blockquote className="mt-1.5 border-l-2 border-line pl-2.5 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-dim">
          {quote}
        </blockquote>
      )}
      {files.length > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-1">
          {files.map((f) => (
            <span
              key={f}
              data-tip="this session had touched the file when the card was captured"
              className="rounded border border-line px-1 py-px font-mono text-[9.5px] text-ink-faint"
            >
              {f}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

/** The card's labels as read-only chips (edited in the create modal). */
function LabelRow({ labels }: { labels: readonly string[] }): JSX.Element | null {
  if (labels.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1">
      {labels.map((l) => {
        const hsl = labelColor(l);
        return (
          <span
            key={l}
            className="rounded-full border px-1.5 py-[1px] font-mono text-[9px]"
            style={{
              borderColor: `hsl(${hsl} / 0.4)`,
              backgroundColor: `hsl(${hsl} / 0.12)`,
              color: `hsl(${hsl})`,
            }}
          >
            {l}
          </span>
        );
      })}
    </div>
  );
}

export function TaskBrief({
  task,
  draft,
  setField,
  commit,
}: {
  task: BoardTask;
  draft: TaskDraft;
  setField: DraftSetter;
  /** Autosave trigger — the modal saves on blur, there is no Save button. */
  commit: () => void;
}): JSX.Element {
  const { title, prompt } = draft;
  const promptRef = useRef<HTMLTextAreaElement>(null);
  useAutosize(promptRef, prompt);
  return (
    <div className="flex flex-col gap-3">
      <div>
        <FieldLabel>title</FieldLabel>
        <input
          type="text"
          value={title}
          onChange={(e) => setField('title', e.target.value)}
          onBlur={commit}
          aria-label="title"
          className="w-full rounded-[8px] border border-line bg-field px-2.5 py-1.5 text-[13px] text-ink outline-none focus:border-ink-dim"
        />
      </div>

      <div>
        <FieldLabel>what needs doing</FieldLabel>
        <textarea
          ref={promptRef}
          value={prompt}
          onChange={(e) => setField('prompt', e.target.value)}
          onBlur={commit}
          rows={2}
          aria-label="prompt"
          className="w-full resize-y rounded-[8px] border border-line bg-field px-2.5 py-1.5 font-mono text-[11.5px] leading-relaxed text-ink outline-none focus:border-ink-dim"
        />
      </div>

      <LabelRow labels={task.labels} />

      <SourceBlock task={task} />
    </div>
  );
}
