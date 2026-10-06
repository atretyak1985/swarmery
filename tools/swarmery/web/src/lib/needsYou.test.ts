// Pure "Needs you" helpers (lib/needsYou.ts). No DOM.
// web/tsconfig.json EXCLUDES *.test.ts, so treat the casts as documentation.
//
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/lib/needsYou.test.ts`.

import { describe, expect, it } from 'vitest';
import type { NeedsYouItem, NeedsYouKind } from '../api/types';
import {
  dismissKey,
  kindLabel,
  oldestFirst,
  REPLY_QUOTE_MAX,
  replyPrefix,
  replyWithOption,
  visibleItems,
} from './needsYou';

function item(patch: Partial<NeedsYouItem> = {}): NeedsYouItem {
  return {
    kind: 'awaiting_reply',
    sessionId: 7,
    sessionUuid: 'u-7',
    sessionName: 'orders refactor',
    projectSlug: 'shop',
    requestId: null,
    toolName: '',
    preview: 'Should I keep the old endpoint?',
    question: 'Should I keep the old endpoint or remove it?',
    asksQuestion: true,
    blockingSince: '2026-10-06T10:00:00Z',
    blockingSeconds: 120,
    termFocusUrl: null,
    suggestion: null,
    ...patch,
  };
}

describe('replyPrefix', () => {
  it('quotes the question under the session name and leaves a blank line for the reply', () => {
    expect(replyPrefix(item())).toBe('[orders refactor] Re: Should I keep the old endpoint or remove it?\n\n');
  });

  it('prefers the suggestion question over the raw paragraph', () => {
    const entry = item({ suggestion: { question: 'Keep the old endpoint?', options: ['keep', 'remove'], recommended: 'keep' } });
    expect(replyPrefix(entry)).toBe('[orders refactor] Re: Keep the old endpoint?\n\n');
  });

  it('falls back to the raw question when the suggestion question is empty', () => {
    const entry = item({ suggestion: { question: '', options: [], recommended: '' } });
    expect(replyPrefix(entry)).toContain('Re: Should I keep the old endpoint or remove it?');
  });

  it('falls back to the preview when there is no question at all', () => {
    expect(replyPrefix(item({ question: '' }))).toBe('[orders refactor] Re: Should I keep the old endpoint?\n\n');
  });

  it('trims and clips the quoted question to 300 characters', () => {
    const long = `  ${'x'.repeat(REPLY_QUOTE_MAX + 50)}  `;
    const out = replyPrefix(item({ question: long }));
    expect(REPLY_QUOTE_MAX).toBe(300);
    expect(out).toBe(`[orders refactor] Re: ${'x'.repeat(300)}\n\n`);
  });

  it('appends an option after the prefix', () => {
    expect(replyWithOption(item(), 'remove it')).toBe(
      '[orders refactor] Re: Should I keep the old endpoint or remove it?\n\nremove it',
    );
  });
});

describe('kindLabel', () => {
  it('names all five kinds', () => {
    const cases: [NeedsYouKind, string][] = [
      ['approval', 'Approval'],
      ['question', 'Question'],
      ['prod_deploy_local', 'Confirm locally'],
      ['awaiting_reply', 'Awaiting your reply'],
      ['failed', 'Failed'],
    ];
    for (const [k, label] of cases) expect(kindLabel(k), k).toBe(label);
  });
});

describe('dismissKey', () => {
  it('is kind:sessionId:blockingSince', () => {
    expect(dismissKey(item())).toBe('awaiting_reply:7:2026-10-06T10:00:00Z');
  });

  it('changes when the same session blocks again', () => {
    expect(dismissKey(item({ blockingSince: '2026-10-06T11:00:00Z' }))).not.toBe(dismissKey(item()));
  });
});

describe('visibleItems / oldestFirst', () => {
  it('drops dismissed episodes only', () => {
    const a = item();
    const b = item({ blockingSince: '2026-10-06T11:00:00Z' });
    expect(visibleItems([a, b], new Set([dismissKey(a)]))).toEqual([b]);
  });

  it('sorts the oldest blocker first and keeps same-instant order', () => {
    const newer = item({ sessionId: 1, blockingSince: '2026-10-06T11:00:00Z' });
    const older = item({ sessionId: 2, blockingSince: '2026-10-06T09:00:00Z' });
    const tieA = item({ sessionId: 3, kind: 'approval', blockingSince: '2026-10-06T10:00:00Z' });
    const tieB = item({ sessionId: 4, kind: 'failed', blockingSince: '2026-10-06T10:00:00Z' });
    expect(oldestFirst([newer, tieA, tieB, older]).map((i) => i.sessionId)).toEqual([2, 3, 4, 1]);
  });
});
