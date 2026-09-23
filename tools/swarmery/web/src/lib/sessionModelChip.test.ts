// Run with:
//   npx vitest run src/lib/sessionModelChip.test.ts
// (vitest is fetched on demand; it is intentionally NOT a committed dependency.)
//
// The bug pinned here is a SILENT one: modelLast/modelChanged rode the session
// DTO with no reader, so a session an Opus 5.5 safeguard moved onto an older
// model displayed the model it started on and nothing said otherwise.

import { describe, expect, it } from 'vitest';
import { modelShortName, sessionModelFallback } from './sessionModelChip';

const base = {
  model: 'claude-opus-5-5',
  modelLast: 'claude-opus-5-5',
  modelChanged: false,
  modelFellBack: false,
};

describe('sessionModelFallback', () => {
  it('claims nothing for a session that never moved', () => {
    expect(sessionModelFallback(base)).toBeNull();
  });

  it('reports BOTH ends of a real move', () => {
    expect(
      sessionModelFallback({
        model: 'claude-opus-5-5',
        modelLast: 'claude-opus-4-1',
        modelChanged: true,
        modelFellBack: true,
      }),
    ).toEqual({ from: 'claude-opus-5-5', to: 'claude-opus-4-1', fellBack: true });
  });

  // The crying-wolf case. An operator escalating mid-session is a CHANGE, not a
  // fallback; the daemon says so via modelFellBack and the chip must carry that
  // through instead of painting every move amber.
  it('does not call an escalation a fallback', () => {
    expect(
      sessionModelFallback({
        model: 'claude-sonnet-5',
        modelLast: 'claude-opus-5-5',
        modelChanged: true,
        modelFellBack: false,
      }),
    ).toEqual({ from: 'claude-sonnet-5', to: 'claude-opus-5-5', fellBack: false });
  });

  it('does not claim a move on a context-window marker alone', () => {
    // The daemon already decides this (modelid.SameTier) and says false; the
    // chip must not re-derive a different answer from the two raw strings.
    expect(
      sessionModelFallback({
        model: 'claude-opus-5-5',
        modelLast: 'claude-opus-5-5[1m]',
        modelChanged: false,
        modelFellBack: false,
      }),
    ).toBeNull();
  });

  it('refuses a half-known move rather than rendering a blank end', () => {
    const f = { modelChanged: true, modelFellBack: true };
    expect(sessionModelFallback({ model: null, modelLast: 'claude-opus-4-1', ...f })).toBeNull();
    expect(sessionModelFallback({ model: 'claude-opus-5-5', modelLast: null, ...f })).toBeNull();
    expect(
      sessionModelFallback({ model: 'claude-opus-5-5', modelLast: 'claude-opus-5-5', ...f }),
    ).toBeNull();
  });
});

describe('modelShortName', () => {
  it('shortens ids it knows and keeps the rest verbatim', () => {
    expect(modelShortName('claude-opus-5-5')).toBe('opus');
    expect(modelShortName('claude-opus-5-5[1m]')).toBe('opus');
    expect(modelShortName('claude-opus-4-1')).toBe('claude-opus-4-1');
  });
});
