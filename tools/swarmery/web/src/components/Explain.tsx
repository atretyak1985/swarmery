// The in-UI explainer affordance: a small "?" (reference) or "!" (needs action)
// circle that opens a popover with the concept's short definition, what to do
// about it, its hard numbers, and a deep link into /docs.
//
// Click-driven, not hover: this is something the user deliberately opens, so it
// is a real <button> with keyboard access and Escape-to-close. Hover-on-dense-
// data-rows stays HoverTip's job.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useId, useRef, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { Link } from 'react-router-dom';
import { CONCEPTS, type ConceptId } from '../lib/glossary';
import { useAnchoredLayer } from './useAnchoredLayer';

// Semantic theme roles, not Tailwind's default palette: --color-amber and
// --color-purple are re-tuned per palette in index.css (light mode darkens them
// to clear AA on a white ground), so these flip with the theme. A hard-coded
// default-palette hue would not — it reads 2.15:1 on the light surface.
const TONE = {
  explain: {
    glyph: '?',
    button: 'border-line text-ink-faint hover:border-ink-dim hover:text-ink-dim',
    eyebrow: 'text-purple',
    card: 'border-purple/30',
  },
  action: {
    glyph: '!',
    button: 'border-amber/50 text-amber hover:border-amber hover:text-amber',
    eyebrow: 'text-amber',
    card: 'border-amber/40',
  },
} as const;

// The circle stays 15px so it sits inside a line of body text; before:-inset-1.5
// grows the pointer/touch target to 27px, clearing the WCAG 2.2 AA 24px floor
// without changing what is drawn.
const TRIGGER_CLASS =
  'relative inline-flex h-[15px] w-[15px] shrink-0 items-center justify-center rounded-full ' +
  'border align-middle font-mono text-[9.5px] leading-none transition-colors ' +
  'before:absolute before:-inset-1.5 before:rounded-full ' +
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/60';

export function Explain({ id }: { id: ConceptId }): JSX.Element {
  const { t } = useLingui();
  const concept = CONCEPTS[id];
  // The term itself is glossary content (lib/glossary.ts, out of i18n scope);
  // only the question around it is this component's own chrome.
  const term = concept.term;
  const tone = TONE[concept.tone];
  const panelId = useId();
  const btnRef = useRef<HTMLButtonElement | null>(null);
  const { anchor, layerRef, openAt, close } = useAnchoredLayer('below');
  const open = anchor !== null;

  const toggle = useCallback(
    (e: React.MouseEvent<HTMLButtonElement>): void => {
      // These chips live inside clickable cards and <Link> rows — never let the
      // explainer double as a navigation click.
      e.preventDefault();
      e.stopPropagation();
      if (open) close();
      else openAt(e.currentTarget.getBoundingClientRect());
    },
    [open, close, openAt],
  );

  // Move focus into the panel when it opens, so the dialog role is not a lie and
  // a keyboard user lands on the content they just asked for. preventScroll
  // matters: any scroll closes the layer (useAnchoredLayer), so a focus-induced
  // scroll would shut the panel the same frame it opened.
  useEffect(() => {
    if (open) layerRef.current?.focus({ preventScroll: true });
  }, [open, layerRef]);

  // Dismissal, following useDropdownDismiss (pages/system/shared.tsx) — Escape
  // closes AND returns focus to the trigger. An outside pointer-down or a focus
  // move deliberately does NOT restore focus: the user is already on their way
  // somewhere else, and yanking focus back would fight what they just reached.
  //
  // The trigger guard in onPointerDown is what stops the CLOSING click from
  // flickering the panel back open: pointerdown on the trigger would close it,
  // and the click that follows would then see open === false and reopen.
  //
  // Escape is listened for in the CAPTURE phase, and that phase is load-bearing.
  // This chip is placed inside workspace/TaskModal.tsx (via PlaybookHint), a
  // drawer that closes itself on a document-level, bubble-phase Escape. In the
  // bubble phase a window listener runs AFTER every document one, so a
  // bubble-phase stopPropagation() here would fire too late and one Escape
  // would close both the popover and the drawer. window is the first node in
  // the capture path, so capture + stopPropagation() makes the topmost layer —
  // this popover — the only thing that consumes the key.
  //
  // Capture is a real cost, not a free win: React 19 delegates synthetic events
  // to the createRoot container, a DESCENDANT of document, so stopping at
  // window/capture also pre-empts every React onKeyDown for that Escape —
  // SessionDetail's title editor (SessionDetail.tsx:87) and the tag input
  // (ProjectActions.tsx:97) among them. That is only sound while this popover
  // genuinely IS the topmost layer, which is what onFocusOut below enforces:
  // the moment focus lands anywhere outside the trigger and the panel — a
  // command palette opened with Cmd+K, an autofocused input, a drawer — the
  // popover closes and stops intercepting anything. Without that guard a
  // popover left open behind a newer layer would silently eat that layer's
  // first Escape. It also rules out two popovers open at once, which
  // onPointerDown alone cannot: Enter/Space fires a click with no pointerdown.
  useEffect(() => {
    if (!open) return;
    const onKey = (ev: KeyboardEvent): void => {
      if (ev.key !== 'Escape') return;
      ev.stopPropagation();
      close();
      btnRef.current?.focus({ preventScroll: true });
    };
    const onPointerDown = (ev: PointerEvent): void => {
      const target = ev.target as Node | null;
      if (target === null) return;
      const inTrigger = btnRef.current?.contains(target) === true;
      const inPanel = layerRef.current?.contains(target) === true;
      if (inTrigger || inPanel) return;
      close();
    };
    const onFocusOut = (ev: FocusEvent): void => {
      // relatedTarget, NOT document.activeElement. During focusout the focus has
      // left the old node and not yet landed, so activeElement reads <body> —
      // and deferring the read by a microtask does not help: the microtask
      // checkpoint runs between focusout and focusin, still on <body>. Measured,
      // not assumed: that version closed the panel on its own trigger's
      // mousedown (so the click that followed re-opened it — the exact flicker
      // onPointerDown's trigger guard exists to prevent) and tore the panel out
      // from under the "Read more" link before its click could navigate.
      // relatedTarget is the node actually receiving focus, available
      // synchronously.
      const next = ev.relatedTarget as Node | null;
      // null means focus left to nothing at all (window blur, devtools). The
      // popover is still the topmost layer in this document — leave it alone.
      if (next === null) return;
      if (btnRef.current?.contains(next) === true) return;
      if (layerRef.current?.contains(next) === true) return;
      close();
    };
    window.addEventListener('keydown', onKey, true);
    window.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('focusout', onFocusOut, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      window.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('focusout', onFocusOut, true);
    };
  }, [open, close, layerRef]);

  const panel = !open
    ? null
    : createPortal(
        <div
          ref={layerRef}
          id={panelId}
          role="dialog"
          aria-label={concept.term}
          tabIndex={-1}
          // A portal escapes the DOM tree but NOT the React tree — clicks in here
          // still bubble to whatever clickable card renders the chip.
          onClick={(e) => {
            e.stopPropagation();
          }}
          className={`fixed z-50 w-[300px] rounded-xl border ${tone.card} bg-surface px-3.5 py-3 opacity-0 shadow-lg transition-opacity duration-100 focus:outline-none`}
        >
          <div className={`font-mono text-[10.5px] tracking-[0.08em] uppercase ${tone.eyebrow}`}>
            {concept.term}
          </div>
          <p className="mt-1.5 text-[12px] leading-relaxed text-ink-2">{concept.short}</p>

          {concept.actions !== undefined && (
            <ul className="mt-2.5 space-y-1 border-t border-line pt-2.5">
              {concept.actions.map((a) => (
                <li key={a} className="flex gap-1.5 text-[11.5px] leading-relaxed text-ink-dim">
                  <span aria-hidden="true" className="text-ink-faint">
                    •
                  </span>
                  <span className="min-w-0">{a}</span>
                </li>
              ))}
            </ul>
          )}

          {/* `steps` is deliberately NOT rendered here. A 300px popover
              reproducing four multi-sentence paragraphs is a panel pretending
              to be a tooltip, and both step-carrying concepts already render
              their walkthrough inline via <HowItWorks> on their own page — so
              the popover would repeat it verbatim a few hundred pixels away.
              The popover is the compression; the inline block is the long
              form; "Read more →" is the full text. */}

          {concept.facts !== undefined && (
            <dl className="mt-2.5 flex flex-wrap gap-x-3 gap-y-1 border-t border-line pt-2.5 font-mono text-[10.5px]">
              {concept.facts.map((f) => (
                <div key={f.label} className="flex gap-1">
                  <dt className="text-ink-faint">{f.label}</dt>
                  <dd className="text-ink-dim">{f.value}</dd>
                </div>
              ))}
            </dl>
          )}

          {concept.doc !== undefined && (
            <Link
              to={`/docs/${concept.doc.slug}#${concept.doc.anchor}`}
              onClick={close}
              className="mt-2.5 inline-block font-mono text-[10.5px] text-brand hover:underline"
            >
              <Trans>Read more →</Trans>
            </Link>
          )}
        </div>,
        document.body,
      );

  return (
    <>
      <button
        ref={btnRef}
        type="button"
        onClick={toggle}
        // A wrapping card (workspace/TaskCard.tsx) preventDefault()s Enter/Space
        // to act on itself, which would swallow the click this button
        // synthesizes and leave the popover unopenable from the keyboard.
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') e.stopPropagation();
        }}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-controls={open ? panelId : undefined}
        aria-label={t`What is ${term}?`}
        className={`${TRIGGER_CLASS} ${tone.button}`}
      >
        {tone.glyph}
      </button>
      {panel}
    </>
  );
}

/**
 * The standard pairing of a chip (or control) with its explainer: one flex row,
 * one gap, decided once here rather than at each boxed call site.
 *
 * Why a wrapper at all: dropped as a bare sibling into a row that already has
 * its own gap, the trigger sits exactly as far from what it explains as from
 * the next chip along, and reads as belonging to either. The wrapper binds it
 * to its subject and leaves the row's own spacing alone.
 *
 * Why gap-2 specifically: TRIGGER_CLASS grows the hit target with
 * `before:-inset-1.5` — 6px in every direction on a `position: relative`
 * element, so that halo hit-tests ABOVE non-positioned siblings. At gap-1 (4px)
 * it overlaps its neighbour by 2px and steals clicks from it; 6px is the exact
 * clearance point, and gap-2 (8px) clears it with 2px to spare.
 *
 * Explainers that flow inside a sentence (Playbooks.tsx `worktree`) do not use
 * this — they are part of the text and need no box.
 */
export function ExplainPair({
  id,
  children,
}: {
  id: ConceptId;
  children: ReactNode;
}): JSX.Element {
  return (
    <span className="inline-flex items-center gap-2">
      {children}
      <Explain id={id} />
    </span>
  );
}
