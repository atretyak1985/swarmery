// Theme picker (Fusion phase 16): mode segmented control (Light/Dark/System) +
// a filterable palette list with 4-swatch preview tiles. Hover previews a
// palette live (transient data-palette on <html>); click commits it through the
// provider (persisted). Rendered two ways:
//   variant="popover" — header gear-style dropdown (default), matches the
//                       NotifySettings hairline language.
//   variant="panel"   — inline card body for /p/:slug/settings.
//
// Live preview writes document.documentElement.dataset.palette directly and
// restores the committed palette on mouse-leave; the provider only re-applies on
// a committed change, so these transient writes are safe.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState } from 'react';
import { useTheme } from '../lib/theme';
import { PALETTES, type Mode, type PaletteId } from './palettes';

const MODES: readonly { v: Mode; label: MessageDescriptor; glyph: string }[] = [
  { v: 'light', label: msg`Light`, glyph: '☀' },
  { v: 'dark', label: msg`Dark`, glyph: '☾' },
  { v: 'system', label: msg`System`, glyph: '⧉' },
];

/** Segmented Light/Dark/System control. */
function ModeSegments({
  mode,
  onPick,
}: {
  mode: Mode;
  onPick: (m: Mode) => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  return (
    <div
      role="radiogroup"
      aria-label={t`color mode`}
      className="flex gap-1 rounded-lg border border-line-strong bg-field p-0.5"
    >
      {MODES.map((m) => {
        const active = mode === m.v;
        return (
          <button
            key={m.v}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onPick(m.v)}
            className={`flex flex-1 items-center justify-center gap-1.5 rounded-[7px] px-2 py-1 font-mono text-[11px] font-semibold transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand ${
              active
                ? 'bg-surface2 text-ink'
                : 'text-ink-dim hover:text-ink'
            }`}
          >
            <span aria-hidden="true">{m.glyph}</span>
            {i18n._(m.label)}
          </button>
        );
      })}
    </div>
  );
}

/** One palette row: 4 swatches + label/hint + active check. */
function PaletteTile({
  id,
  label,
  hint,
  swatches,
  active,
  onCommit,
  onPreview,
  onEndPreview,
}: {
  id: PaletteId;
  label: string;
  hint: string;
  swatches: readonly [string, string, string, string];
  active: boolean;
  onCommit: (id: PaletteId) => void;
  onPreview: (id: PaletteId) => void;
  onEndPreview: () => void;
}): JSX.Element {
  const { t } = useLingui();
  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      aria-label={t`${label} palette — ${hint}`}
      onClick={() => onCommit(id)}
      onMouseEnter={() => onPreview(id)}
      onMouseLeave={onEndPreview}
      onFocus={() => onPreview(id)}
      onBlur={onEndPreview}
      className={`flex w-full items-center gap-2.5 rounded-[9px] border px-2 py-1.5 text-left transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand ${
        active
          ? 'border-line-strong bg-surface2'
          : 'border-transparent hover:border-line hover:bg-surface2'
      }`}
    >
      <span
        aria-hidden="true"
        className="flex shrink-0 overflow-hidden rounded-[6px] border border-line-strong"
      >
        {swatches.map((c, i) => (
          <span key={i} className="h-5 w-3" style={{ background: c }} />
        ))}
      </span>
      <span className="flex min-w-0 flex-1 flex-col leading-tight">
        <span className="font-mono text-[11.5px] font-semibold text-ink">{label}</span>
        <span className="truncate text-[10px] text-ink-faint">{hint}</span>
      </span>
      <span
        aria-hidden="true"
        className={`shrink-0 font-mono text-[12px] ${active ? 'text-brand' : 'text-transparent'}`}
      >
        ✓
      </span>
    </button>
  );
}

/** Shared body: mode segments + filter + palette list. */
function PickerBody(): JSX.Element {
  const { mode, palette, setMode, setPalette } = useTheme();
  const { t, i18n } = useLingui();
  const [filter, setFilter] = useState('');

  // Live hover/focus preview: write the transient palette straight to <html>,
  // restore the committed one when the pointer leaves.
  const preview = (id: PaletteId): void => {
    document.documentElement.dataset.palette = id;
  };
  const endPreview = (): void => {
    document.documentElement.dataset.palette = palette;
  };
  // Keep the DOM in sync if the committed palette changes while open.
  useEffect(() => {
    document.documentElement.dataset.palette = palette;
  }, [palette]);

  // Palette names and hints are messages (theme/palettes.ts): the filter
  // matches what the operator reads, i.e. the strings in the active locale.
  const q = filter.trim().toLowerCase();
  const translated = PALETTES.map((p) => ({ ...p, label: i18n._(p.label), hint: i18n._(p.hint) }));
  const visible = translated.filter(
    (p) => q === '' || p.label.toLowerCase().includes(q) || p.hint.toLowerCase().includes(q),
  );

  return (
    <>
      <ModeSegments mode={mode} onPick={setMode} />
      <div className="mt-2.5">
        <input
          type="text"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          placeholder={t`filter palettes…`}
          aria-label={t`filter palettes`}
          className="w-full rounded-[7px] border border-line-strong bg-field px-2 py-1 font-mono text-[11px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-ink-dim"
        />
      </div>
      <div
        role="radiogroup"
        aria-label={t`palette`}
        className="mt-1.5 flex flex-col gap-0.5"
        onMouseLeave={endPreview}
      >
        {visible.map((p) => (
          <PaletteTile
            key={p.id}
            id={p.id}
            label={p.label}
            hint={p.hint}
            swatches={p.swatches}
            active={palette === p.id}
            onCommit={setPalette}
            onPreview={preview}
            onEndPreview={endPreview}
          />
        ))}
        {visible.length === 0 && (
          <div className="px-2 py-2 font-mono text-[11px] text-ink-faint">
            <Trans>no palette matches</Trans>
          </div>
        )}
      </div>
    </>
  );
}

/** Inline settings-card variant (no popover chrome). */
export function ThemePickerPanel(): JSX.Element {
  return (
    <div className="rounded-xl border border-line bg-surface p-3">
      <div className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
        <Trans>appearance</Trans>
      </div>
      <div className="mt-2.5">
        <PickerBody />
      </div>
    </div>
  );
}

/** Header popover variant (default). */
export function ThemePicker(): JSX.Element {
  const { theme } = useTheme();
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return undefined;
    const onDown = (e: MouseEvent): void => {
      if (ref.current !== null && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={t`theme settings`}
        data-tip={t`theme`}
        className="flex h-[26px] w-[26px] shrink-0 items-center justify-center rounded-lg border border-line bg-field text-[13px] leading-none text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
      >
        <span aria-hidden="true">{theme === 'light' ? '☾' : '☀'}</span>
      </button>
      {open && (
        <div
          role="dialog"
          aria-label={t`theme settings`}
          className="absolute right-0 z-30 mt-2 w-[268px] rounded-xl border border-line bg-surface p-3"
        >
          <div className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
            <Trans>appearance</Trans>
          </div>
          <div className="mt-2.5">
            <PickerBody />
          </div>
        </div>
      )}
    </div>
  );
}
