// Small design-system primitives shared across screens (Canvas dark editorial
// language: JetBrains Mono uppercase eyebrows, hairline pill status chips,
// warm near-black hairline cards).

import { useEffect, useRef, type ReactNode } from 'react';
import type { SessionStatus } from '../api/types';
import { fmtSpan } from '../lib/format';

/* ----- shared eyebrow rhythm — one token for every section heading -----
 * Canvas section rhythm: the SAME vertical breathing applies to the mono
 * eyebrows ("The spine · today", rail headers) and the mono day-group rules
 * on Sessions, so all screens breathe identically. */

export const EYEBROW_SPACING = 'mt-[26px] mb-2.5';

/* ----- section heading — the Canvas JetBrains Mono uppercase eyebrow ----- */

export function SectionTitle({
  children,
  flush = false,
}: {
  children: ReactNode;
  /** Align with a sibling card top instead of the standard 26px rhythm
   * (Docs rail — the eyebrow sits beside content, not between sections). */
  flush?: boolean;
}): JSX.Element {
  return (
    <h2
      className={`${flush ? 'mt-1 mb-2.5' : EYEBROW_SPACING} font-mono text-[11px] font-medium tracking-[0.16em] text-ink-dim uppercase`}
    >
      {children}
    </h2>
  );
}

/* ----- mono group header — day rules on Sessions ("today · sun, jul 12 ·
 * 9 sessions" + trailing hairline), same rhythm as SectionTitle ----- */

export function GroupHeader({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div
      className={`${EYEBROW_SPACING} flex items-center gap-2 font-mono text-[10.5px] tracking-[0.14em] text-ink-faint uppercase`}
    >
      {children}
      <span className="h-px flex-1 bg-line" aria-hidden="true" />
    </div>
  );
}

/* ----- session-table column templates (≥900px) -----
 * One column system shared by the Sessions day groups and the Overview
 * "Recently completed" card so both read as the same table:
 *   [status dot] [project] [title 1fr] [model] [branch] [start] [duration]
 * Overview drops the status-dot and branch columns (completed rows). */

/* Fixed widths everywhere (no max-content): each row is its own grid, so any
 * content-sized column would resolve per row and the table would shear —
 * durations like "active · 7 h 06 min" vs "37 s" must not move the columns. */
export const SESSION_ROW_GRID =
  'desk:grid-cols-[14px_120px_minmax(0,1fr)_130px_60px_44px_150px]';
export const COMPLETED_ROW_GRID =
  'desk:grid-cols-[120px_minmax(0,1fr)_130px_44px_150px]';

/* ----- duration pill — right column of session table rows -----
 * Active rows get the green-tinted "active · 3 h 43 min" pill; everything
 * else is a plain hairline duration ("37 s", "69 h 29 min"). */

export function DurationPill({
  status,
  startedAt,
  endedAt,
}: {
  status: SessionStatus;
  startedAt: string;
  endedAt: string | null;
}): JSX.Element {
  const span = fmtSpan(startedAt, endedAt);
  if (status === 'active') {
    return (
      <span className="rounded-full border border-green/40 bg-green/10 px-2 py-0.5 font-mono text-[10.5px] whitespace-nowrap text-green">
        active · {span}
      </span>
    );
  }
  return (
    <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10.5px] whitespace-nowrap text-ink-dim">
      {span}
    </span>
  );
}

/* ----- card shell — raised navy on page bg, 12px radius, hairline ----- */

export function Card({
  children,
  className = '',
}: {
  children: ReactNode;
  className?: string;
}): JSX.Element {
  return (
    <div className={`mb-2.5 rounded-xl border border-line bg-surface px-3.5 py-3 ${className}`}>
      {children}
    </div>
  );
}

/* ----- async states ----- */

export function Loading({ label = 'loading…' }: { label?: string }): JSX.Element {
  return (
    <div className="flex items-center gap-2.5 py-10 text-ink-dim justify-center" role="status">
      <span className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line border-t-brand" />
      <span className="font-mono text-[12px]">{label}</span>
    </div>
  );
}

export function ErrorBox({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}): JSX.Element {
  return (
    <div className="my-3 rounded-lg border border-red/25 bg-red/5 px-3.5 py-3" role="alert">
      <div className="font-mono text-[12px] text-red">{message}</div>
      {onRetry !== undefined && (
        <button
          type="button"
          onClick={onRetry}
          className="mt-2 rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors hover:bg-surface2"
        >
          retry
        </button>
      )}
    </div>
  );
}

/**
 * Honesty hint for stats endpoints whose range overlaps pruned (rolled-up)
 * days: daily rollups keep aggregates only, so per-event breakdowns
 * (tools, error groups) undercount there. Mirrors the backend `approx` flag.
 */
export function ApproxHint(): JSX.Element {
  return (
    <p className="mt-2 font-mono text-[10px] text-amber/80">
      ≈ approximate — this range overlaps pruned days (daily rollups), so older detail is missing
    </p>
  );
}

export function Empty({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div className="my-3 rounded-xl border border-dashed border-line px-3.5 py-6 text-center text-[12.5px] text-ink-dim">
      {children}
    </div>
  );
}

/* ----- focus containment — one Tab trap for every overlay -----
 * aria-modal tells assistive tech the rest of the page is unavailable, so Tab
 * must not walk into whatever is behind the overlay (still in the DOM). Queried
 * live on each keypress so controls that appear or disable mid-edit count. */

export const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), iframe, [tabindex]:not([tabindex="-1"])';

/** Wraps Tab / Shift+Tab around the focusable elements inside `box`, and pulls
 * focus back in when it has escaped. No-op for any other key. */
export function containTab(e: KeyboardEvent, box: HTMLElement): void {
  if (e.key !== 'Tab') return;
  const focusable = [...box.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)];
  const first = focusable[0];
  const last = focusable[focusable.length - 1];
  if (first === undefined || last === undefined) return;
  const active = document.activeElement;
  if (!e.shiftKey && (active === last || !box.contains(active))) {
    e.preventDefault();
    first.focus();
  } else if (e.shiftKey && (active === first || !box.contains(active))) {
    e.preventDefault();
    last.focus();
  }
}

/* ----- confirmation dialog (phase 4 step-12) -----
 * Destructive actions (hook disable, rollback, delete, conflict reload) must
 * be deliberate: a fixed overlay + hairline card. The destructive confirm
 * button follows the Approvals deny-button style; cancel is the plain
 * hairline secondary. Render null while closed.
 *
 * It is usually raised OVER another modal (a discard-changes confirm on top of
 * the form it guards), so it owns the keyboard while open:
 *   - focus moves to the SAFE button (cancel) on open, and returns to whatever
 *     had it before on close — Enter never lands on a covered control;
 *   - Tab is contained to the dialog;
 *   - Esc cancels THIS dialog only. The listener is a window CAPTURE listener
 *     that stops propagation, so it runs before, and instead of, the parent's
 *     own Esc / Tab handlers (window bubble listeners or React onKeyDown);
 *     preventDefault also tells an ExpandableSection underneath to stand down;
 *   - a backdrop click cancels and stops there — React bubbles it through the
 *     component tree, and a parent whose backdrop calls its own close request
 *     would otherwise re-open the confirm in the same batch. */

export function ConfirmDialog(props: ConfirmDialogProps): JSX.Element | null {
  // The body mounts per open, so its effects run exactly on open and on close.
  return props.open ? <ConfirmDialogBody {...props} /> : null;
}

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  children: ReactNode;
  confirmLabel: string;
  /** Approvals deny-button tones for destructive confirms. */
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

function ConfirmDialogBody({
  title,
  children,
  confirmLabel,
  danger = false,
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps): JSX.Element {
  const boxRef = useRef<HTMLDivElement | null>(null);
  const cancelRef = useRef<HTMLButtonElement | null>(null);
  // Latest handlers without re-running the mount effect (callers pass arrows).
  const cancelHandler = useRef(onCancel);
  const busyRef = useRef(busy);
  useEffect(() => {
    cancelHandler.current = onCancel;
    busyRef.current = busy;
  });

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    cancelRef.current?.focus();
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopImmediatePropagation();
        if (!busyRef.current) cancelHandler.current();
        return;
      }
      if (e.key !== 'Tab') return;
      const box = boxRef.current;
      if (box === null) return;
      e.stopImmediatePropagation();
      containTab(e, box);
    };
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      // The opener may be gone (the confirm discarded the form it lived in).
      if (opener?.isConnected === true) opener.focus();
    };
  }, []);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={title}
      onClick={(e) => {
        e.stopPropagation();
        if (!busy) onCancel();
      }}
    >
      <div
        ref={boxRef}
        className="w-full max-w-md rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="font-display text-[14px] font-bold text-ink">{title}</div>
        <div className="mt-2 text-[12.5px] leading-relaxed text-ink-2">{children}</div>
        <div className="mt-3.5 flex flex-wrap justify-end gap-2">
          <button
            ref={cancelRef}
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
          >
            cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={busy}
            className={`rounded-lg border px-3.5 py-1.5 font-mono text-[11.5px] font-semibold transition-colors disabled:opacity-50 ${
              danger
                ? 'border-red/40 bg-red/10 text-red hover:bg-red/20'
                : 'border-green/40 bg-green/10 text-green hover:bg-green/20'
            }`}
          >
            {busy ? '…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

/* ----- expandable section — fullscreen without remounting the children -----
 * Embedded pages (architecture map, graphify viz, serena panes, docs) hand this
 * wrapper a HEAVY child — an iframe or a canvas that carries state the user is
 * mid-way through: pan/zoom, scroll position, a loaded document, a graph
 * layout. So expanding has to be a class change and nothing else.
 *
 * INVARIANT: `children` render from ONE subtree in both states. No conditional
 * render of the children, no `key` that varies with `expanded`, no portal.
 * Move them into a branch and React unmounts the old tree, which reloads every
 * iframe and throws away exactly the state that was being looked at — the same
 * wrapper-only rule pages/Architecture.tsx relies on so its fullscreen map does
 * not re-fetch. The close button is a SIBLING placed after the children, so it
 * never shifts their position in the tree.
 *
 * LAYERING: the expanded overlay is z-[45] — above the app header (z-20,
 * App.tsx) and above the in-flow modals at z-40 (workspace/TaskModal.tsx,
 * components/usage/UsageModal.tsx), below the dialog layer at z-50 (Modal
 * below, workspace/NewTaskModal.tsx) and below tooltips at z-[60]
 * (components/Tooltip.tsx). A fill route never has a z-40 modal open at the
 * same time, so 45 only has to clear the header; it is deliberately under 50 so
 * a real dialog can still be raised over an expanded section.
 *
 * The `defaultPrevented` guards below are what that case hangs on: ConfirmDialog
 * above claims Escape in a window capture listener and calls preventDefault()
 * (and stops propagation), so it wins over this section. No current consumer
 * of this component raises a dialog while expanded. Whoever opens any OTHER
 * dialog over an expanded section must give it an Escape handler that calls
 * preventDefault(), or Esc will collapse the section UNDER the dialog instead
 * of closing it. */

/** Trigger styling for the expand affordance, exported so a page that needs its
 * own label/placement still matches every other embedded page. */
export const EXPAND_BUTTON_CLASS =
  'rounded-[9px] border border-line-strong bg-field px-2.5 py-[6px] font-mono text-[12px] text-ink transition-colors hover:border-ink-dim';

/** The standard `⛶ expand` trigger — one glyph and one word, everywhere. */
export function ExpandButton({
  onClick,
  label = 'expand',
  expanded,
  className = EXPAND_BUTTON_CLASS,
}: {
  onClick: () => void;
  /** Overrides the word after the glyph (the accessible name follows it). */
  label?: string;
  /** Drives aria-expanded, like every other disclosure trigger in the app. */
  expanded?: boolean;
  /** Re-skins the trigger for a page whose header buttons are lighter than a
   * toolbar control (planning). The SEMANTICS — decorative glyph, accessible
   * name, aria-expanded — stay here so a re-skin cannot drift away from them. */
  className?: string;
}): JSX.Element {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-expanded={expanded}
      className={className}
    >
      {/* The glyph is decoration — hiding it keeps the accessible name equal to
          the visible word instead of "⛶ expand". */}
      <span aria-hidden="true">⛶ </span>
      {label}
    </button>
  );
}

/* Body scroll lock, shared by every expanded section.
 *
 * Saving and restoring per instance breaks as soon as two sections exist: A
 * locks (captures ''), B locks (captures 'hidden'), A collapses and writes ''
 * back — the page behind scrolls again while B is still fullscreen. One
 * module-level refcount owns the original value: captured on 0→1, restored on
 * 1→0, so nesting and out-of-order collapse both stay correct. */
let scrollLocks = 0;
let scrollLockPrev = '';

function lockBodyScroll(): void {
  if (scrollLocks === 0) {
    scrollLockPrev = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
  }
  scrollLocks += 1;
}

function unlockBodyScroll(): void {
  scrollLocks = Math.max(0, scrollLocks - 1);
  if (scrollLocks === 0) document.body.style.overflow = scrollLockPrev;
}

/* Esc ownership. One window listener per expanded section would collapse ALL of
 * them on a single Esc; the stack makes the most recently expanded one the only
 * responder, which is what "Esc closes the thing on top" means. */
const escStack: object[] = [];

export function ExpandableSection({
  expanded,
  onToggle,
  label,
  children,
  className,
}: {
  expanded: boolean;
  onToggle: (next: boolean) => void;
  /** Names the overlay for screen readers while expanded (aria-label). */
  label: string;
  children: ReactNode;
  /** Extra classes for the collapsed wrapper; the overlay owns its own box. */
  className?: string;
}): JSX.Element {
  // onToggle may change identity on every render (callers pass inline arrows);
  // call the latest one without re-running the effect below, which would
  // re-capture the focus anchor and re-read the body overflow it must restore.
  // Written in an effect, not during render — a render-phase ref write is what
  // React warns about, and the handler below only ever fires post-commit.
  const toggleRef = useRef(onToggle);
  useEffect(() => {
    toggleRef.current = onToggle;
  });

  const boxRef = useRef<HTMLDivElement | null>(null);
  const closeRef = useRef<HTMLButtonElement | null>(null);

  // While expanded: Esc collapses (this section only, if several are open), the
  // page behind is scroll-locked through the shared refcount, focus moves to the
  // close button and returns to the element that opened the overlay on collapse
  // so keyboard navigation resumes where it left off instead of at the document
  // top. pages/Architecture.tsx still carries its own older copy of this
  // behaviour (at z-[60], and it never restores focus); the Architecture phase
  // deletes that copy in favour of this one, dropping its overlay to z-[45].
  useEffect(() => {
    if (!expanded) return;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const owner = {};
    escStack.push(owner);

    // Only the topmost expanded section answers Esc.
    const collapseIfTopmost = (): void => {
      if (escStack[escStack.length - 1] === owner) toggleRef.current(false);
    };

    const onKey = (e: KeyboardEvent): void => {
      // A dialog raised OVER an expanded section (z-50 > z-[45]) gets Esc first;
      // if it handled the key, this section must not also collapse behind it.
      if (e.defaultPrevented) return;
      if (e.key === 'Escape') {
        collapseIfTopmost();
        return;
      }
      if (e.key !== 'Tab') return;
      // Focus containment. aria-modal tells assistive tech the rest of the page
      // is unavailable, so Tab must not walk into the shell behind — it is still
      // in the DOM. Containment stops at an iframe boundary by construction:
      // once focus is inside a frame, its document owns Tab and the host cannot
      // see it. That is acceptable here (the frame IS the content) and is why
      // the close button is also reachable by mouse and by Esc.
      const box = boxRef.current;
      if (box === null) return;
      containTab(e, box);
    };

    window.addEventListener('keydown', onKey);

    // Backstop for every way focus can leave the overlay that the Tab handler
    // above cannot see: tabbing BACKWARDS out of a child frame (the frame's
    // document owns that key, and the browser then walks to the host element
    // before the <iframe> — outside the overlay), and any programmatic focus
    // from the shell behind. aria-modal promises the rest of the page is
    // unavailable, so focus is pulled back to the close button. Only the topmost
    // section may do this, or two open sections would fight over the focus.
    const onFocusIn = (e: FocusEvent): void => {
      if (escStack[escStack.length - 1] !== owner) return;
      const box = boxRef.current;
      if (box === null) return;
      const target = e.target;
      if (target !== null && !box.contains(target as Node)) closeRef.current?.focus();
    };
    document.addEventListener('focusin', onFocusIn);

    // A keydown inside an iframe fires in the frame's own document and never
    // crosses into ours, so Esc would die the moment the user clicked into the
    // map or graph — the normal state while expanded. Same-origin frames get a
    // listener of their own; a cross-origin one (serena's dashboard) cannot, by
    // design.
    //
    // It handles Escape ONLY. Tab belongs to whichever document the key was
    // pressed in: the frame runs its own tab order, and from the host side
    // `document.activeElement` is just the <iframe>, so reusing the containment
    // handler here would cancel the frame's navigation and yank focus out to the
    // close button on the first Shift+Tab inside the map. (Filtering by
    // `e.target.ownerDocument` does not work either — an element from the frame
    // is not an `instanceof` the HOST realm's Node, so the check silently fails
    // open. Two handlers is the honest fix.)
    //
    // Attaching once is not enough: a frame still loading at expand time holds a
    // throwaway about:blank, and the architecture map re-navigates its frame on
    // every rebuild (cache-busted src). Both replace the document and would drop
    // the listener with it, so the host-side `load` event re-attaches — it fires
    // for same-origin navigations and is the only signal the host reliably gets.
    const onFrameKey = (e: KeyboardEvent): void => {
      if (e.defaultPrevented) return;
      if (e.key === 'Escape') collapseIfTopmost();
    };
    const framesDocs = new Set<Document>();
    const attachToFrame = (frame: HTMLIFrameElement): void => {
      try {
        const doc = frame.contentDocument;
        if (doc === null || framesDocs.has(doc)) return;
        doc.addEventListener('keydown', onFrameKey);
        framesDocs.add(doc);
      } catch {
        // Cross-origin frame — the host is not allowed to listen inside it.
      }
    };
    const frames = [...(boxRef.current?.querySelectorAll('iframe') ?? [])];
    const onFrameLoad = (e: Event): void => {
      if (e.currentTarget instanceof HTMLIFrameElement) attachToFrame(e.currentTarget);
    };
    for (const frame of frames) {
      attachToFrame(frame);
      frame.addEventListener('load', onFrameLoad);
    }

    lockBodyScroll();
    closeRef.current?.focus();

    return () => {
      window.removeEventListener('keydown', onKey);
      document.removeEventListener('focusin', onFocusIn);
      for (const frame of frames) frame.removeEventListener('load', onFrameLoad);
      for (const doc of framesDocs) doc.removeEventListener('keydown', onFrameKey);
      const at = escStack.lastIndexOf(owner);
      if (at !== -1) escStack.splice(at, 1);
      unlockBodyScroll();
      // On unmount-while-expanded the opener is often detached; focus() is then
      // a harmless no-op. Do not "fix" this into a document.body.focus() — and
      // when the section mounted already expanded there IS no opener but <body>,
      // which would drop keyboard position to the document top for nothing.
      if (opener !== null && opener !== document.body) opener.focus();
    };
  }, [expanded]);

  // Reset the OWNER's flag when an expanded section stops being rendered.
  //
  // Every consumer gates this section on the pane still existing — a serena
  // project that is running, a graphify project that has a viz, an architecture
  // map that built. When that condition flips while expanded (the project
  // stops, the map is deleted), the section unmounts: the effect above cleans
  // up correctly, so scroll unlocks and the listeners go. But `expanded` lives
  // in the PAGE, and nothing has told it — so the flag stays true, the trigger
  // that comes back still reads aria-expanded="true", and the next time the
  // pane renders the section mounts straight into fullscreen over a pane the
  // user never asked to expand.
  //
  // Fixing it here rather than in each page is the point: the invariant is
  // "collapsed is the only state this can be resurrected in", and it belongs to
  // the primitive that owns the state machine. An empty dep list is what makes
  // this possible — its cleanup runs on unmount ONLY, the one case the
  // [expanded] effect above cannot tell apart from an ordinary collapse (both
  // run the same cleanup).
  const expandedRef = useRef(expanded);
  useEffect(() => {
    expandedRef.current = expanded;
  });
  useEffect(
    () => () => {
      if (expandedRef.current) toggleRef.current(false);
    },
    [],
  );

  const collapsedClass =
    className === undefined
      ? 'flex min-h-0 flex-1 flex-col'
      : `flex min-h-0 flex-1 flex-col ${className}`;

  return (
    <div
      ref={boxRef}
      className={expanded ? 'fixed inset-0 z-[45] flex flex-col bg-bg p-3 desk:p-4' : collapsedClass}
      role={expanded ? 'dialog' : undefined}
      aria-modal={expanded ? true : undefined}
      aria-label={expanded ? label : undefined}
    >
      {children}
      {expanded && (
        <button
          type="button"
          ref={closeRef}
          onClick={() => onToggle(false)}
          aria-label={`collapse ${label}`}
          className="absolute top-5 right-6 rounded-[9px] border border-line-strong bg-surface px-2.5 py-[6px] font-mono text-[12px] text-ink shadow-lg transition-colors hover:border-ink-dim desk:top-6 desk:right-7"
        >
          ✕ close
        </button>
      )}
    </div>
  );
}
