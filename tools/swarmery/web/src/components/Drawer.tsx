// Right-side overlay drawer (Canvas v3, artboard 2d): a phase, a session or a
// lesson opens OVER its list instead of replacing it, so the list is never lost.
// Generalised from pages/planning/HistoryDrawer.tsx.
//
// Keyboard and focus contract:
//   - the drawer is a modal dialog (role="dialog" aria-modal), labelled by its
//     title; focus moves into it on open and returns to the opener on close
//     (WCAG 2.2 §2.4.3 Focus Order);
//   - Tab is contained inside it (ui.tsx containTab — one trap for every overlay);
//   - Esc closes; a backdrop click closes;
//   - with onPrev/onNext, ArrowUp/ArrowDown step to the neighbouring item
//     ("↑↓ next phase"), except while typing in a field.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useId, useRef, type ReactNode } from 'react';
import { containTab } from './ui';

export interface DrawerProps {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  /** Mono meta line above the title ("Phase 3 of 4 · 6/8 criteria"). */
  subtitle?: ReactNode;
  /** Panel width in px (capped at the viewport). */
  width?: number;
  children: ReactNode;
  /** Action row pinned to the bottom. */
  footer?: ReactNode;
  onPrev?: () => void;
  onNext?: () => void;
}

export function Drawer(props: DrawerProps): JSX.Element | null {
  // The body mounts per open, so its effects run exactly on open and on close.
  return props.open ? <DrawerBody {...props} /> : null;
}

/** True when a key press belongs to a text field, not to the drawer. */
function typingIn(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName);
}

function DrawerBody({
  onClose,
  title,
  subtitle,
  width = 640,
  children,
  footer,
  onPrev,
  onNext,
}: DrawerProps): JSX.Element {
  const { t } = useLingui();
  const panelRef = useRef<HTMLDivElement | null>(null);
  const titleId = useId();
  // Latest handlers without re-running the listener effect (callers pass arrows).
  const handlers = useRef({ onClose, onPrev, onNext });
  useEffect(() => {
    handlers.current = { onClose, onPrev, onNext };
  });

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    panelRef.current?.focus();
    const onKey = (e: KeyboardEvent): void => {
      const panel = panelRef.current;
      if (panel === null) return;
      if (e.key === 'Escape') {
        e.preventDefault();
        handlers.current.onClose();
        return;
      }
      if (e.key === 'Tab') {
        containTab(e, panel);
        return;
      }
      if (typingIn(e.target)) return;
      const { onPrev: prev, onNext: next } = handlers.current;
      if (e.key === 'ArrowUp' && prev !== undefined) {
        e.preventDefault();
        prev();
      } else if (e.key === 'ArrowDown' && next !== undefined) {
        e.preventDefault();
        next();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('keydown', onKey);
      opener?.focus();
    };
  }, []);

  const stepping = onPrev !== undefined || onNext !== undefined;

  return (
    <div className="fixed inset-0 z-50 flex justify-end bg-bg/60" onClick={onClose}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        style={{ width }}
        onClick={(e) => e.stopPropagation()}
        className="flex h-full max-w-full flex-col border-l border-line bg-surface shadow-[-24px_0_48px_rgba(0,0,0,0.5)] outline-none"
      >
        <div className="border-b border-line px-[22px] pt-4 pb-3.5">
          <div className="flex items-center gap-2 font-mono text-[10px] text-ink-faint">
            {subtitle !== undefined && <span className="min-w-0 truncate">{subtitle}</span>}
            <button
              type="button"
              onClick={onClose}
              aria-label={t`close`}
              className="ml-auto rounded px-1 transition-colors hover:text-ink"
            >
              {/* i18n-ignore: the Escape key's name, printed on the keycap */}
              esc
            </button>
          </div>
          <h2 id={titleId} className="mt-1.5 text-[17px] leading-[1.3] font-medium text-ink">
            {title}
          </h2>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-[22px] py-[18px]">{children}</div>
        {(footer !== undefined || stepping) && (
          <div className="flex flex-wrap items-center gap-2 border-t border-line px-[22px] py-3.5">
            {footer}
            {stepping && (
              <span className="ml-auto font-mono text-[10.5px] text-ink-faint">
                <Trans>↑↓ next</Trans>
              </span>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
