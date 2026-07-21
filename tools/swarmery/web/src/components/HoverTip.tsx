// Lightweight hover tooltip: a styled details card rendered through a portal
// with fixed positioning (never clipped by the rail's overflow), placed to the
// LEFT of the anchor (the rail hugs the right viewport edge) and clamped to
// the viewport. Pointer-only supplement — the same details stay reachable by
// expanding the row / opening the Timeline, so no focus handling is needed.

import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';

const SHOW_DELAY_MS = 150;

export function useHoverTip(content: JSX.Element): {
  handlers: {
    onMouseEnter: (e: React.MouseEvent<HTMLElement>) => void;
    onMouseLeave: () => void;
  };
  portal: JSX.Element | null;
} {
  const [anchor, setAnchor] = useState<DOMRect | null>(null);
  const timer = useRef<number | null>(null);
  const tipRef = useRef<HTMLDivElement | null>(null);

  const onMouseEnter = (e: React.MouseEvent<HTMLElement>): void => {
    const rect = e.currentTarget.getBoundingClientRect();
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      setAnchor(rect);
    }, SHOW_DELAY_MS);
  };
  const onMouseLeave = (): void => {
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = null;
    setAnchor(null);
  };

  useEffect(() => {
    return () => {
      if (timer.current !== null) window.clearTimeout(timer.current);
    };
  }, []);

  // Any scroll invalidates the captured anchor rect — just hide.
  useEffect(() => {
    if (anchor === null) return;
    const hide = (): void => {
      setAnchor(null);
    };
    window.addEventListener('scroll', hide, true);
    return () => {
      window.removeEventListener('scroll', hide, true);
    };
  }, [anchor]);

  // Position after render, once the tip's real size is measurable.
  useLayoutEffect(() => {
    const tip = tipRef.current;
    if (anchor === null || tip === null) return;
    const { offsetWidth: w, offsetHeight: h } = tip;
    let left = anchor.left - w - 10;
    if (left < 8) left = Math.min(anchor.right + 10, window.innerWidth - w - 8);
    const top = Math.max(8, Math.min(anchor.top + anchor.height / 2 - h / 2, window.innerHeight - h - 8));
    tip.style.left = `${String(left)}px`;
    tip.style.top = `${String(top)}px`;
    tip.style.opacity = '1';
  }, [anchor]);

  const portal =
    anchor === null
      ? null
      : createPortal(
          <div
            ref={tipRef}
            role="tooltip"
            className="pointer-events-none fixed z-50 w-[264px] opacity-0 transition-opacity duration-100"
          >
            {content}
          </div>,
          document.body,
        );

  return { handlers: { onMouseEnter, onMouseLeave }, portal };
}
