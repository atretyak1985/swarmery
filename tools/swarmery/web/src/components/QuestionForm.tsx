// Shared AskUserQuestion answer UI (fusion phase 8): the per-question option
// list (radio / checkbox) + «own answer» free-text input, extracted from the
// Approvals PendingCard so BOTH the Approvals screen and the Planning page render
// identical question controls without duplication. QuestionBlock is the single
// question; QuestionForm wraps a whole set with draft state + a submit button
// (used inline on the Planning page — Approvals keeps its own richer card chrome
// and reuses only QuestionBlock).

import { useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import {
  buildAnswers,
  EMPTY_DRAFT,
  type AnswerDraft,
  type AnswerMap,
  type ParsedQuestion,
} from '../lib/approvals';

/** One AskUserQuestion question: options + «own answer» free text. */
export function QuestionBlock({
  question,
  index,
  draft,
  group,
  onToggle,
  onFreeText,
}: {
  question: ParsedQuestion;
  index: number;
  draft: AnswerDraft;
  /** Radio/checkbox group namespace — unique per card and question. */
  group: string;
  onToggle: (label: string) => void;
  onFreeText: (text: string) => void;
}): JSX.Element {
  const { t } = useLingui();
  return (
    <fieldset className="rounded-[10px] border border-line px-3 py-2.5">
      <legend className="px-1 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
        {question.header !== '' ? question.header : `question ${String(index + 1)}`}
        {question.multiSelect ? ' · multi' : ''}
      </legend>
      <div className="mt-[5px] text-[13px] leading-snug text-ink">{question.question}</div>
      <div className="mt-2 flex flex-col gap-[3px]">
        {question.options.map((opt) => (
          <label
            key={opt.label}
            className="flex min-h-11 cursor-pointer items-baseline gap-[9px] rounded-[7px] px-[7px] py-[5px] transition-colors hover:bg-surface2"
          >
            <input
              type={question.multiSelect ? 'checkbox' : 'radio'}
              name={group}
              checked={draft.selected.includes(opt.label)}
              onChange={() => onToggle(opt.label)}
              className="translate-y-px accent-green focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
            />
            <span className="font-mono text-[11.5px] whitespace-nowrap text-ink">{opt.label}</span>
            {opt.description !== '' && (
              <span className="min-w-0 flex-1 text-[11.5px] leading-snug text-ink-dim">
                {opt.description}
              </span>
            )}
          </label>
        ))}
      </div>
      <input
        type="text"
        value={draft.freeText}
        onChange={(e) => onFreeText(e.target.value)}
        placeholder={
          question.multiSelect
            ? t`own answer — added to the selection`
            : t`own answer — overrides the selection`
        }
        aria-label={`own answer for "${question.question}"`}
        className="mt-1.5 w-full rounded-lg border border-line bg-field px-2.5 py-[5px] font-mono text-[11.5px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-green/40"
      />
    </fieldset>
  );
}

/**
 * A whole AskUserQuestion set with local draft state and a submit button.
 * `idNamespace` scopes the radio/checkbox groups so multiple forms on one page
 * do not collide. onSubmit receives the built AnswerMap (never null — the button
 * is disabled until every question is answered). Used inline on the Planning
 * page; the Approvals card composes QuestionBlock directly with its own submit.
 */
export function QuestionForm({
  questions,
  idNamespace,
  busy = false,
  submitLabel = 'submit answers',
  onSubmit,
}: {
  questions: readonly ParsedQuestion[];
  idNamespace: string;
  busy?: boolean;
  submitLabel?: string;
  onSubmit: (answers: AnswerMap) => void;
}): JSX.Element {
  const [drafts, setDrafts] = useState<readonly AnswerDraft[]>(() => questions.map(() => EMPTY_DRAFT));
  const answers = buildAnswers(questions, drafts);

  const updateDraft = (i: number, patch: Partial<AnswerDraft>): void => {
    setDrafts((prev) => prev.map((d, j) => (j === i ? { ...d, ...patch } : d)));
  };
  const toggleOption = (i: number, q: ParsedQuestion, label: string): void => {
    const draft = drafts[i] ?? EMPTY_DRAFT;
    if (!q.multiSelect) {
      updateDraft(i, { selected: [label] });
      return;
    }
    updateDraft(i, {
      selected: draft.selected.includes(label)
        ? draft.selected.filter((l) => l !== label)
        : [...draft.selected, label],
    });
  };

  return (
    <div className="flex flex-col gap-2.5">
      {questions.map((q, i) => (
        <QuestionBlock
          key={q.question}
          question={q}
          index={i}
          draft={drafts[i] ?? EMPTY_DRAFT}
          group={`${idNamespace}-${String(i)}`}
          onToggle={(label) => toggleOption(i, q, label)}
          onFreeText={(text) => updateDraft(i, { freeText: text })}
        />
      ))}
      <button
        type="button"
        disabled={busy || answers === null}
        onClick={() => {
          if (answers !== null) onSubmit(answers);
        }}
        className="self-start rounded-lg border border-green/45 bg-green/12 px-4 py-[7px] font-mono text-[11.5px] font-bold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
      >
        {submitLabel}
      </button>
    </div>
  );
}
