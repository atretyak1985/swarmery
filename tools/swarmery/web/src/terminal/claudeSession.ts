// Binds a "Claude Code" dock tab to one Claude session. The tab mints the
// session's UUID itself, so it types `claude --session-id <uuid>` the first
// time and `claude --resume <uuid>` once the daemon has ingested that session:
// Restart and Start continue the same conversation instead of opening an empty
// one, and the tab can take its name from the daemon's session title.

/** The command a "Claude Code" dock tab runs in its shell (the tab-kind marker). */
export const CLAUDE_COMMAND = 'claude';

/** The two TermTab fields the command depends on. */
export interface ClaudeTabFields {
  run?: string | undefined;
  sessionId?: string | undefined;
}

/** A fresh Claude session UUID for a new tab. */
export function newSessionId(): string {
  return crypto.randomUUID();
}

/**
 * The command a tab types into its shell. A Claude tab with a session id
 * resumes that session when the daemon knows it (`known`), and otherwise
 * creates it under that id — Claude Code refuses `--session-id` for a UUID
 * already in use. Any other tab returns its `run` unchanged; a Claude tab with
 * no id yet (a tab saved before sessions were bound) falls back to bare
 * `claude`.
 */
export function claudeCommandFor(tab: ClaudeTabFields, known: boolean): string | undefined {
  if (tab.run !== CLAUDE_COMMAND) return tab.run;
  const id = tab.sessionId;
  if (id === undefined || id === '') return CLAUDE_COMMAND;
  return known ? `${CLAUDE_COMMAND} --resume ${id}` : `${CLAUDE_COMMAND} --session-id ${id}`;
}

/** Leading glyphs that are not part of a title's words (✳, *, #, quotes, dashes). */
const LEADING_NON_WORD = /^[^\p{L}\p{N}]+/u;

/**
 * A short tab caption from a session title: at most `maxWords` words and
 * `maxChars` characters (the ellipsis included), cut on a word boundary, with
 * `…` appended whenever anything was dropped. Leading non-letter glyphs are
 * stripped. Returns null for a missing or empty title.
 */
export function shortTitle(
  title: string | null | undefined,
  maxWords = 3,
  maxChars = 32,
): string | null {
  if (title === null || title === undefined) return null;
  const words = title.trim().replace(LEADING_NON_WORD, '').split(/\s+/).filter((w) => w !== '');
  if (words.length === 0) return null;

  let kept = words.slice(0, Math.max(1, maxWords));
  let cut = kept.length < words.length;
  const fits = (ws: string[]): boolean => ws.join(' ').length + (cut ? 1 : 0) <= maxChars;
  while (kept.length > 1 && !fits(kept)) {
    kept = kept.slice(0, -1);
    cut = true;
  }
  let text = kept.join(' ');
  if (!fits(kept)) {
    // One word longer than the budget: cut inside it.
    text = text.slice(0, Math.max(1, maxChars - 1));
    cut = true;
  }
  return cut ? `${text}…` : text;
}
