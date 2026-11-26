// The resize handle between two neighbours of a dock split (terminal-dock-webstorm
// phase 4). It fills the gutter layoutRects() leaves between children `index` and
// `index + 1`; dragging it, or ←/→ (↑/↓ in a column) while it has focus, moves
// share between exactly those two children. The pair's total never changes, so
// every other child keeps its size, and both stay at or above layout.ts's 0.1
// floor. The new sizes go to resizeSplit through `onResize`.

import { useRef } from 'react';
import type { Rect, SplitDir } from './layout';

/** Share moved by one arrow key press. */
export const KEY_STEP = 0.05;
/** layout.ts's floor for a child's share (resizeSplit clamps to it too). */
const MIN_SHARE = 0.1;

/**
 * `sizes` with `delta` moved from child `index + 1` to child `index`, both
 * clamped to the floor so the pair's total stays put.
 */
export function shiftShare(sizes: readonly number[], index: number, delta: number): number[] {
  const a = sizes[index];
  const b = sizes[index + 1];
  if (a === undefined || b === undefined) return [...sizes];
  const pair = a + b;
  const nextA = Math.min(pair - MIN_SHARE, Math.max(MIN_SHARE, a + delta));
  return sizes.map((v, i) => (i === index ? nextA : i === index + 1 ? pair - nextA : v));
}

export function SplitHandle({
  dir,
  rect,
  index,
  sizes,
  extent,
  label,
  onResize,
}: {
  /** The split's direction: `row` children sit side by side, so the handle drags horizontally. */
  dir: SplitDir;
  /** The gutter's box inside the dock body, in px. */
  rect: Rect;
  /** The handle sits between children `index` and `index + 1`. */
  index: number;
  /** The split's current (normalized) sizes. */
  sizes: readonly number[];
  /** The px length the split's sizes share (its extent minus every gutter). */
  extent: number;
  label: string;
  onResize: (sizes: number[]) => void;
}): JSX.Element {
  const row = dir === 'row';
  const dragRef = useRef<{ start: number; sizes: readonly number[] } | null>(null);

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>): void => {
    if (e.button !== 0) return;
    e.preventDefault();
    dragRef.current = { start: row ? e.clientX : e.clientY, sizes: [...sizes] };
    try {
      e.currentTarget.setPointerCapture(e.pointerId);
    } catch {
      /* no pointer capture here (jsdom, a synthetic pointer) — moves still arrive while over the handle */
    }
  };
  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>): void => {
    const d = dragRef.current;
    if (d === null || extent <= 0) return;
    const delta = ((row ? e.clientX : e.clientY) - d.start) / extent;
    onResize(shiftShare(d.sizes, index, delta));
  };
  const onPointerUp = (e: React.PointerEvent<HTMLDivElement>): void => {
    dragRef.current = null;
    try {
      e.currentTarget.releasePointerCapture(e.pointerId);
    } catch {
      /* capture was never taken */
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>): void => {
    const back = row ? 'ArrowLeft' : 'ArrowUp';
    const forward = row ? 'ArrowRight' : 'ArrowDown';
    if (e.key !== back && e.key !== forward) return;
    e.preventDefault();
    onResize(shiftShare(sizes, index, e.key === forward ? KEY_STEP : -KEY_STEP));
  };

  const share = sizes[index] ?? 0;
  const pair = share + (sizes[index + 1] ?? 0);
  return (
    // A focusable separator is a widget: it takes the arrow keys itself (an
    // <hr> can take neither focus nor a pointer drag).
    <div
      role="separator"
      aria-label={label}
      // A row split's handle is a vertical bar, and the reverse.
      aria-orientation={row ? 'vertical' : 'horizontal'}
      aria-valuenow={Math.round(share * 100)}
      aria-valuemin={Math.round(MIN_SHARE * 100)}
      aria-valuemax={Math.round((pair - MIN_SHARE) * 100)}
      tabIndex={0}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onKeyDown={onKeyDown}
      data-testid="split-handle"
      className={`pointer-events-auto absolute z-10 bg-line transition-colors hover:bg-brand/60 focus-visible:bg-brand focus-visible:outline-none ${
        row ? 'cursor-col-resize' : 'cursor-row-resize'
      }`}
      style={{ left: rect.x, top: rect.y, width: rect.w, height: rect.h }}
    />
  );
}
