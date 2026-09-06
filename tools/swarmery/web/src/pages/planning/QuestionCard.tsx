// Structured wizard question (interactive planning v2 — phase 3): radio group
// for single_select, checkboxes for multi_select, per-option description,
// collapsible pros/cons, a "recommended" badge on the option the planner would
// pick itself, and an "Other" option that reveals a textarea. Local
// selection state only — the parent owns submission. Render with
// key={question.id} so a new question resets the selection.

import { useState } from 'react';
import type { PlanningQuestion } from '../../api/types';

export function QuestionCard({
  question,
  busy,
  onSubmit,
}: {
  question: PlanningQuestion;
  busy: boolean;
  onSubmit: (selected: string[], otherText?: string) => void;
}): JSX.Element {
  const [selected, setSelected] = useState<string[]>([]);
  const [otherText, setOtherText] = useState('');

  const single = question.type === 'single_select';

  const toggle = (id: string): void => {
    setSelected((prev) => {
      if (single) return [id];
      return prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id];
    });
  };

  // "Other" needs non-empty text before the answer is submittable.
  const otherSelected = question.options.some((o) => o.isOther === true && selected.includes(o.id));
  const valid = selected.length > 0 && (!otherSelected || otherText.trim() !== '');

  return (
    <form
      className="rounded-xl border border-line bg-surface px-4 py-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (!valid || busy) return;
        onSubmit(selected, otherSelected && otherText.trim() !== '' ? otherText.trim() : undefined);
      }}
    >
      <div className="mb-1 font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase">
        the planner is asking
      </div>
      <div
        id={`planning-q-label-${question.id}`}
        className="font-display text-[15px] font-semibold text-ink"
      >
        {question.question}
      </div>
      {question.description !== undefined && question.description !== '' && (
        <p className="mt-1.5 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-dim">
          {question.description}
        </p>
      )}

      <div
        className="mt-3.5 space-y-2"
        role={single ? 'radiogroup' : 'group'}
        aria-labelledby={`planning-q-label-${question.id}`}
      >
        {question.options.map((opt) => {
          const checked = selected.includes(opt.id);
          const recommended = opt.recommended === true && opt.isOther !== true;
          const hasProsCons =
            (opt.pros !== undefined && opt.pros.length > 0) ||
            (opt.cons !== undefined && opt.cons.length > 0);
          return (
            <div
              key={opt.id}
              className={`rounded-[10px] border px-3 py-2.5 transition-colors ${
                checked
                  ? 'border-brand/50 bg-brand/6'
                  : recommended
                    ? 'border-brand/30 bg-brand/[0.03] hover:border-brand/50'
                    : 'border-line bg-bg hover:border-line-strong'
              }`}
            >
              <label className="flex cursor-pointer items-start gap-2.5">
                <input
                  type={single ? 'radio' : 'checkbox'}
                  name={`planning-q-${question.id}`}
                  value={opt.id}
                  checked={checked}
                  onChange={() => toggle(opt.id)}
                  disabled={busy}
                  className="mt-[3px] h-3.5 w-3.5 shrink-0 accent-brand"
                />
                <span className="min-w-0">
                  <span className="flex flex-wrap items-center gap-2 text-[13px] font-semibold text-ink">
                    {opt.label}
                    {recommended && (
                      <span
                        className="rounded border border-brand/40 bg-brand/12 px-1.5 py-px font-mono text-[10px] font-medium tracking-[0.06em] text-brand uppercase"
                        title="the option the planner would pick itself"
                      >
                        recommended
                      </span>
                    )}
                  </span>
                  {opt.description !== undefined && opt.description !== '' && (
                    <span className="mt-0.5 block text-[12px] leading-relaxed whitespace-pre-wrap text-ink-dim">
                      {opt.description}
                    </span>
                  )}
                </span>
              </label>

              {hasProsCons && (
                <details className="mt-1.5 ml-6">
                  <summary className="cursor-pointer font-mono text-[10.5px] text-ink-faint select-none hover:text-ink-2">
                    Pros / Cons
                  </summary>
                  <div className="mt-1.5 grid gap-2 sm:grid-cols-2">
                    {opt.pros !== undefined && opt.pros.length > 0 && (
                      <ul className="space-y-1">
                        {opt.pros.map((p) => (
                          <li key={p} className="flex gap-1.5 text-[11.5px] leading-relaxed text-ink-2">
                            <span aria-hidden="true" className="shrink-0 font-mono text-green">
                              +
                            </span>
                            {p}
                          </li>
                        ))}
                      </ul>
                    )}
                    {opt.cons !== undefined && opt.cons.length > 0 && (
                      <ul className="space-y-1">
                        {opt.cons.map((c) => (
                          <li key={c} className="flex gap-1.5 text-[11.5px] leading-relaxed text-ink-2">
                            <span aria-hidden="true" className="shrink-0 font-mono text-red">
                              −
                            </span>
                            {c}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                </details>
              )}

              {opt.isOther === true && checked && (
                <textarea
                  value={otherText}
                  onChange={(e) => setOtherText(e.target.value)}
                  rows={3}
                  placeholder="describe your own direction…"
                  aria-label="describe your own direction"
                  disabled={busy}
                  className="mt-2 ml-6 w-[calc(100%-1.5rem)] resize-y rounded-lg border border-line bg-field px-2.5 py-2 text-[12.5px] leading-relaxed text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-brand/50 disabled:opacity-50"
                />
              )}
            </div>
          );
        })}
      </div>

      <div className="mt-3.5 flex items-center gap-2.5">
        <button
          type="submit"
          disabled={busy || !valid}
          className="rounded-lg border border-brand/50 bg-brand/12 px-4 py-2 text-[13px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
        >
          {busy ? 'sending…' : 'Submit answer'}
        </button>
        <span className="font-mono text-[10.5px] text-ink-faint">
          {single ? 'pick one' : 'pick any that apply'}
        </span>
      </div>
    </form>
  );
}
