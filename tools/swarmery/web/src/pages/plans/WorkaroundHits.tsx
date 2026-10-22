// The places where a phase's agent said it changed the plan (verify-not-trust
// phase 5, D23): workaround words — "instead", "for now", "pre-existing",
// "поки що" — found in the agent's own prose, each with the sentence before and
// after, inline on the phase card. A hint for the human (the reviewer gets the
// same list as "look first"); nothing gates on it.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { WorkaroundHit } from '../../api/types';

/** How many hits show before "Show all" — the card stays a card. */
export const HITS_FOLDED = 5;

/** Splits a sentence around every case-insensitive occurrence of the matched
 *  word (a phrase's words may be separated by any whitespace), so the word can
 *  be marked. Falls back to the whole sentence when it is not found verbatim. */
export function markWord(sentence: string, word: string): { text: string; hit: boolean }[] {
  const parts = word.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return [{ text: sentence, hit: false }];
  const pattern = parts.map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('\\s+');
  const re = new RegExp(pattern, 'giu');
  const out: { text: string; hit: boolean }[] = [];
  let last = 0;
  for (const m of sentence.matchAll(re)) {
    const at = m.index ?? 0;
    if (at > last) out.push({ text: sentence.slice(last, at), hit: false });
    out.push({ text: m[0], hit: true });
    last = at + m[0].length;
  }
  if (last < sentence.length) out.push({ text: sentence.slice(last), hit: false });
  return out.length > 0 ? out : [{ text: sentence, hit: false }];
}

export interface WorkaroundHitsProps {
  phaseId: number;
  hits: WorkaroundHit[];
  /** Every hit, including the ones past the per-session cap kept without text. */
  total: number;
}

export function WorkaroundHits({ phaseId, hits, total }: WorkaroundHitsProps): JSX.Element | null {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  // Stepping to another phase folds the list again.
  useEffect(() => {
    setOpen(false);
  }, [phaseId]);
  if (hits.length === 0 && total === 0) return null;
  const shown = open ? hits : hits.slice(0, HITS_FOLDED);
  const count = String(total);
  const allCount = String(hits.length);
  const capped = String(total - hits.length);
  return (
    <section
      aria-label={t`Places where the agent changed the plan`}
      className="rounded-md border border-amber/30 bg-amber/5 px-3 py-2.5"
    >
      <h3 className="font-mono text-[10px] tracking-[0.14em] text-amber uppercase">
        <Trans>Changed the plan · {count}</Trans>
      </h3>
      <p className="mt-1 text-[11px] text-ink-dim">
        <Trans>
          The agent&apos;s own words where it went around something. Read these first; each is a place to check, not a
          verdict.
        </Trans>
      </p>
      <ul className="mt-2 grid gap-2">
        {shown.map((h) => (
          <li key={h.id} className="text-[12px] leading-snug">
            <div className="flex flex-wrap items-center gap-1.5 font-mono text-[10px] text-ink-faint">
              <span className="rounded border border-amber/40 px-1.5 py-px text-[9px] text-amber">{h.word}</span>
              {h.turnSeq > 0 && <span>{t`turn ${h.turnSeq}`}</span>}
              {h.agentName !== '' && <span>{h.agentName}</span>}
            </div>
            {h.before !== '' && <p className="mt-0.5 text-ink-faint">{h.before}</p>}
            <p className="mt-0.5 text-ink-2">
              {markWord(h.sentence, h.word).map((part, i) =>
                part.hit ? (
                  <mark key={i} className="rounded-sm bg-amber/25 px-0.5 text-ink">
                    {part.text}
                  </mark>
                ) : (
                  <span key={i}>{part.text}</span>
                ),
              )}
            </p>
            {h.after !== '' && <p className="mt-0.5 text-ink-faint">{h.after}</p>}
          </li>
        ))}
      </ul>
      {hits.length > HITS_FOLDED && (
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className="mt-2 font-mono text-[10px] text-brand hover:underline"
        >
          {open ? t`Show fewer` : t`Show all ${allCount}`}
        </button>
      )}
      {total > hits.length && (
        <p className="mt-2 font-mono text-[10px] text-ink-faint">
          <Trans>{capped} more past the per-session cap — counted, not stored.</Trans>
        </p>
      )}
    </section>
  );
}
