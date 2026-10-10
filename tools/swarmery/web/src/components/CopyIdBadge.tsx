// A copyable id chip: the board card id and the plan id both need to be
// visible at a glance AND pasteable into a conversation that isn't this UI —
// that's the whole point of showing an id at all. Before this, the id sat in
// an inert, near-invisible (`text-ink-faint`, 9px) span; this makes it legible
// and turns the whole chip into the copy target (bigger hit area than a
// separate button would give).
//
// Every host renders this INSIDE its own clickable row (a card, a list
// button, a modal header a click elsewhere closes) — so a click here must
// never bubble into that ancestor's handler. `stopPropagation` always;
// `preventDefault` too when the ancestor is a real `<button>`, where a click
// on a nested control still counts as "inside the button" for form/label
// semantics.
//
// The id text is the READOUT, not just a control's label — swapping it out
// for "copied" (the first cut of this component did) destroys the very thing
// the user was looking at, reflows every sibling chip twice, and fires the
// live region on the id text itself (so it re-announces the bare id, out of
// context, when the confirmation reverts). Follow the split
// `usage/UsageSetupHint.tsx` already uses: the id stays put, only a trailing
// glyph and a separate, normally-empty live region change.

import { useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState } from 'react';

// navigator.clipboard does not exist at all on a non-secure origin (plain
// HTTP on anything but localhost/127.0.0.1 — a LAN hostname like
// `swarmery.local` counts as non-secure even though it resolves to
// loopback, because Chrome's secure-context check is on the literal
// hostname, not where it resolves). The old `?.writeText(id)` here silently
// short-circuited the whole chain in that case — no error, no copy, no
// visual change, just a dead button. execCommand('copy') is deprecated but
// still works everywhere the Clipboard API doesn't, so it's the fallback,
// not the primary path.
async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // fall through to the legacy path below
    }
  }
  // select() moves focus into the off-screen textarea; once it is removed focus
  // would fall to <body>, and a keyboard user loses their place on the card.
  const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.style.position = 'fixed';
  textarea.style.top = '-1000px';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  textarea.select();
  textarea.setSelectionRange(0, text.length);
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  document.body.removeChild(textarea);
  previous?.focus();
  return ok;
}

export function CopyIdBadge({
  id,
  label,
  className = '',
  truncate = false,
}: {
  id: string;
  label?: string;
  className?: string;
  /** Let the id itself shrink with an ellipsis instead of forcing the row to
   * scroll — for hosts (the Plans list row) whose column is narrower than a
   * long `yyyy-mm-dd-slug` plan id. Short ids (a board card's `T-xxxxxx`)
   * don't need this and should leave it off. */
  truncate?: boolean;
}): JSX.Element {
  const { t } = useLingui();
  const what = label ?? t`id`;
  // idle → ok | failed → idle. A failed copy must be as visible as a
  // successful one: a silent no-op is the dead button this chip replaced.
  const [state, setState] = useState<'idle' | 'ok' | 'failed'>('idle');
  const resetTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (resetTimer.current !== null) clearTimeout(resetTimer.current);
    },
    [],
  );

  return (
    <button
      type="button"
      onClick={(e) => {
        // Every call site nests this inside a clickable ancestor (card,
        // list-row button, modal header). Stop the click from opening,
        // navigating, or closing whatever that ancestor does on click.
        e.stopPropagation();
        e.preventDefault();
        void copyText(id).then((ok) => {
          if (resetTimer.current !== null) clearTimeout(resetTimer.current);
          setState(ok ? 'ok' : 'failed');
          resetTimer.current = setTimeout(() => setState('idle'), ok ? 1500 : 2500);
        });
      }}
      aria-label={t`copy id: ${id}`}
      data-tip={t`copy ${what}: ${id}`}
      className={`inline-flex max-w-full items-center gap-1 rounded border px-1 py-[1px] font-mono text-[10px] transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand ${
        truncate ? 'min-w-0' : ''
      } ${
        state === 'ok'
          ? 'border-green/40 bg-green/10 text-green'
          : state === 'failed'
            ? 'border-red/40 bg-red/10 text-red'
            : 'border-line text-ink-2 hover:border-line-strong hover:text-ink'
      } ${className}`}
    >
      <span className={truncate ? 'min-w-0 truncate' : ''}>{id}</span>
      {/* Fixed-width glyph slot — swapping ⧉ ↔ ✓ must not change the chip's
          width, or every sibling chip in a flex-wrap row reflows twice per
          copy. */}
      <span aria-hidden="true" className="inline-block w-[9px] shrink-0 text-center">
        {state === 'ok' ? '✓' : state === 'failed' ? '✕' : '⧉'}
      </span>
      {/* Empty at rest; announced once on copy, then cleared — never holds the
       * id itself, so it can never re-announce it out of context. */}
      <span aria-live="polite" className="sr-only">
        {state === 'ok' ? t`copied` : state === 'failed' ? t`copy failed` : ''}
      </span>
    </button>
  );
}
