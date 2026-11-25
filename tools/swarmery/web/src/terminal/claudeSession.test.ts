import { describe, expect, it } from 'vitest';
import { CLAUDE_COMMAND, claudeCommandFor, newSessionId, shortTitle } from './claudeSession';

const UUID = '3f2b9c1e-8d4a-4f6b-9a1c-2e7d5b8f0a11';

describe('shortTitle', () => {
  it('keeps at most three words and marks the cut', () => {
    expect(shortTitle('Fix the flaky planrun test')).toBe('Fix the flaky…');
  });

  it('keeps a short title whole', () => {
    expect(shortTitle('Refactor dock')).toBe('Refactor dock');
    expect(shortTitle('  Add WebStorm splits  ')).toBe('Add WebStorm splits');
  });

  it('strips leading glyphs', () => {
    expect(shortTitle('✳ Refactor dock')).toBe('Refactor dock');
    expect(shortTitle('# "Ship it"')).toBe('Ship it"');
    expect(shortTitle('** Note')).toBe('Note');
  });

  it('returns null for an empty or missing title', () => {
    expect(shortTitle('')).toBeNull();
    expect(shortTitle('   ')).toBeNull();
    expect(shortTitle('✳ ')).toBeNull();
    expect(shortTitle(null)).toBeNull();
    expect(shortTitle(undefined)).toBeNull();
  });

  it('trims a long three-word title on a word boundary', () => {
    const title = 'Internationalization accessibility performance';
    expect(title.length).toBeGreaterThan(32);
    const short = shortTitle(title);
    expect(short).toBe('Internationalization…');
    expect((short ?? '').length).toBeLessThanOrEqual(32);
  });

  it('cuts inside a single word longer than the budget', () => {
    const short = shortTitle('a'.repeat(40));
    expect(short).toBe(`${'a'.repeat(31)}…`);
  });

  it('honours custom limits', () => {
    expect(shortTitle('one two three four', 2)).toBe('one two…');
    expect(shortTitle('alpha beta gamma', 3, 11)).toBe('alpha beta…');
  });
});

describe('claudeCommandFor', () => {
  it('creates an unknown session under its id', () => {
    expect(claudeCommandFor({ run: CLAUDE_COMMAND, sessionId: UUID }, false)).toBe(`claude --session-id ${UUID}`);
  });

  it('resumes a session the daemon knows', () => {
    expect(claudeCommandFor({ run: CLAUDE_COMMAND, sessionId: UUID }, true)).toBe(`claude --resume ${UUID}`);
  });

  it('returns a non-Claude tab command unchanged', () => {
    expect(claudeCommandFor({ run: 'npm test' }, true)).toBe('npm test');
    expect(claudeCommandFor({}, false)).toBeUndefined();
  });

  it('falls back to bare claude for a tab without a session id', () => {
    expect(claudeCommandFor({ run: CLAUDE_COMMAND }, false)).toBe('claude');
  });
});

describe('newSessionId', () => {
  it('mints a distinct UUID each time', () => {
    const a = newSessionId();
    expect(a).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(newSessionId()).not.toBe(a);
  });
});
