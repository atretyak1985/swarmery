// Theme provider (Fusion phase 16): mode (light/dark/system) + curated palette,
// plus the two v4 display preferences: compact rows and state-glyph motion. Those
// two have no <html> attribute: ListRow and StateGlyph read them from this context.
//
// Persists `{mode, palette, compact, motion}` as JSON to localStorage
// `swarmery.theme` and applies three attributes on <html>:
//   data-mode    — the RESOLVED mode (system → light|dark), the CSS source of
//                  truth for the light overrides in index.css.
//   data-theme   — mirror of data-mode, kept for any legacy/external reader.
//   data-palette — the active palette id (index.css `[data-palette]` blocks).
//
// `system` follows `prefers-color-scheme` LIVE via a matchMedia listener. The
// pre-paint snippet in index.html applies the same attributes before first paint
// (no flash); this provider re-reads the identical stored state on init so
// hydration never flips the theme. localStorage is guarded (private mode leaves
// storage throwing — the in-memory choice still applies for the session), the
// same try/catch idiom as scope.tsx.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import {
  DEFAULT_MODE,
  DEFAULT_PALETTE,
  isMode,
  isPaletteId,
  type Mode,
  type PaletteId,
  type ResolvedMode,
} from '../theme/palettes';

/** Back-compat alias: `theme` used to be the resolved light|dark mode; existing
 *  consumers (App toggle, Analytics chart tokens, WorkspaceShell) still read it. */
export type Theme = ResolvedMode;

const STORAGE_KEY = 'swarmery.theme';

interface ThemeChoice {
  mode: Mode;
  palette: PaletteId;
  /** Tighter rows in every list: ListRow takes 6px of vertical padding, not 9px. */
  compact: boolean;
  /** Spinning and pulsing state glyphs. */
  motion: boolean;
}

const DEFAULT_CHOICE: ThemeChoice = {
  mode: DEFAULT_MODE,
  palette: DEFAULT_PALETTE,
  compact: false,
  motion: true,
};

interface ThemeValue {
  /** User's mode choice (may be `system`). */
  mode: Mode;
  /** Active palette id. */
  palette: PaletteId;
  /** Mode actually applied to the DOM (system already resolved). */
  theme: ResolvedMode;
  /** Tighter list rows; persisted locally. */
  compact: boolean;
  /** State glyphs animate; persisted locally. */
  motion: boolean;
  setMode: (next: Mode) => void;
  setPalette: (next: PaletteId) => void;
  setCompact: (next: boolean) => void;
  setMotion: (next: boolean) => void;
  /** Legacy light↔dark flip. On `system` it snaps to the opposite of the
   *  currently-resolved mode (leaving `system` for an explicit choice). */
  toggle: () => void;
}

const ThemeContext = createContext<ThemeValue>({
  mode: DEFAULT_MODE,
  palette: DEFAULT_PALETTE,
  theme: 'dark',
  compact: DEFAULT_CHOICE.compact,
  motion: DEFAULT_CHOICE.motion,
  setMode: () => undefined,
  setPalette: () => undefined,
  setCompact: () => undefined,
  setMotion: () => undefined,
  toggle: () => undefined,
});

function readStored(): ThemeChoice {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (raw === null) return DEFAULT_CHOICE;
    // Legacy value: a bare 'light' | 'dark' string (pre-phase-16).
    if (raw === 'light' || raw === 'dark') return { ...DEFAULT_CHOICE, mode: raw };
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== 'object' || parsed === null) return DEFAULT_CHOICE;
    const rec = parsed as Record<string, unknown>;
    return {
      mode: isMode(rec.mode) ? rec.mode : DEFAULT_MODE,
      palette: isPaletteId(rec.palette) ? rec.palette : DEFAULT_PALETTE,
      compact: rec.compact === true,
      // Absent means on: a stored choice from before this field keeps its motion.
      motion: rec.motion !== false,
    };
  } catch {
    return DEFAULT_CHOICE; // storage/JSON disabled
  }
}

function persist(choice: ThemeChoice): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(choice));
  } catch {
    // storage disabled — the in-memory choice still applies this session
  }
}

function systemPrefersDark(): boolean {
  try {
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
  } catch {
    return true; // matchMedia unavailable → dark stays the app default
  }
}

function resolveMode(mode: Mode, systemDark: boolean): ResolvedMode {
  if (mode === 'system') return systemDark ? 'dark' : 'light';
  return mode;
}

function applyAttributes(resolved: ResolvedMode, palette: PaletteId): void {
  const root = document.documentElement;
  root.dataset.mode = resolved;
  root.dataset.theme = resolved; // legacy mirror
  root.dataset.palette = palette;
  // The pre-paint snippet set an inline hex background on <html> to avoid a
  // flash before the stylesheet loaded. Now that index.css is present, hand the
  // html background back to the live token so a runtime theme switch never
  // leaves a stale color peeking behind the shell / scroll overflow.
  root.style.background = 'var(--color-bg)';
}

export function ThemeProvider({ children }: { children: ReactNode }): JSX.Element {
  const [choice, setChoice] = useState<ThemeChoice>(readStored);
  const [systemDark, setSystemDark] = useState<boolean>(systemPrefersDark);

  const { mode, palette, compact, motion } = choice;
  const resolved = resolveMode(mode, systemDark);

  // Track the OS preference live so `system` follows a theme switch without a
  // reload. The listener stays mounted regardless of mode (cheap) so switching
  // TO system picks up the current value immediately.
  useEffect(() => {
    let mql: MediaQueryList;
    try {
      mql = window.matchMedia('(prefers-color-scheme: dark)');
    } catch {
      return undefined; // matchMedia unavailable — systemDark stays at default
    }
    const onChange = (e: MediaQueryListEvent): void => setSystemDark(e.matches);
    mql.addEventListener('change', onChange);
    return () => mql.removeEventListener('change', onChange);
  }, []);

  // Apply attributes on mount and whenever the resolved mode or palette changes.
  useEffect(() => {
    applyAttributes(resolved, palette);
  }, [resolved, palette]);

  const update = useCallback(
    (patch: Partial<ThemeChoice>): void => {
      const next = { ...choice, ...patch };
      setChoice(next);
      persist(next);
    },
    [choice],
  );

  const setMode = useCallback((next: Mode): void => update({ mode: next }), [update]);
  const setPalette = useCallback((next: PaletteId): void => update({ palette: next }), [update]);
  const setCompact = useCallback((next: boolean): void => update({ compact: next }), [update]);
  const setMotion = useCallback((next: boolean): void => update({ motion: next }), [update]);

  const toggle = useCallback((): void => {
    setMode(resolved === 'light' ? 'dark' : 'light');
  }, [resolved, setMode]);

  const value = useMemo(
    () => ({ mode, palette, theme: resolved, compact, motion, setMode, setPalette, setCompact, setMotion, toggle }),
    [mode, palette, resolved, compact, motion, setMode, setPalette, setCompact, setMotion, toggle],
  );
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeValue {
  return useContext(ThemeContext);
}
