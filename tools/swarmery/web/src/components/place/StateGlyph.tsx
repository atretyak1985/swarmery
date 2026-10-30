// The one state glyph (handoff §4): a mono character in an 18px box, shape first
// and colour second, so a state still reads without colour. Colour is allowed
// here because this IS state; category never gets one.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { useTheme } from '../../lib/theme';

export type GlyphState =
  | 'needs'
  | 'working'
  | 'done'
  | 'failed'
  | 'stopped'
  | 'pending'
  /** A guard stop in a list: the stopped square, in red. */
  | 'guard'
  /** A manual check in a list. */
  | 'manual'
  /** Needs-you rows name what is being asked. */
  | 'permission'
  | 'question';

interface GlyphDef {
  glyph: string;
  tone: string;
  motion: string;
  label: MessageDescriptor;
}

const GLYPHS: Record<GlyphState, GlyphDef> = {
  needs: { glyph: '●', tone: 'text-amber', motion: 'animate-blink-dot', label: msg`needs you` },
  working: { glyph: '✽', tone: 'text-green', motion: 'animate-glyph-spin', label: msg`working` },
  done: { glyph: '✓', tone: 'text-ink-dim', motion: '', label: msg`done` },
  failed: { glyph: '✗', tone: 'text-red', motion: '', label: msg`failed` },
  stopped: { glyph: '■', tone: 'text-ink-faint', motion: '', label: msg`stopped` },
  pending: { glyph: '○', tone: 'text-ink-faint', motion: '', label: msg`pending` },
  guard: { glyph: '■', tone: 'text-red', motion: '', label: msg`guard stop` },
  manual: { glyph: '☐', tone: 'text-ink-dim', motion: '', label: msg`manual check` },
  permission: { glyph: '▲', tone: 'text-amber', motion: '', label: msg`permission` },
  question: { glyph: '?', tone: 'text-amber', motion: '', label: msg`question` },
};

export function StateGlyph({
  state,
  size = 13,
  className = '',
}: {
  state: GlyphState;
  /** The list row's 13px by default; the ledgers and needs-you rows go smaller. */
  size?: 11 | 12 | 13;
  className?: string;
}): JSX.Element {
  const { i18n } = useLingui();
  const { motion } = useTheme();
  const def = GLYPHS[state];
  return (
    <span
      role="img"
      aria-label={i18n._(def.label)}
      data-state={state}
      style={{ fontSize: size }}
      className={`inline-block w-[18px] origin-center text-center font-mono leading-[18px] ${def.tone} ${motion ? def.motion : ''} ${className}`}
    >
      {def.glyph}
    </span>
  );
}
