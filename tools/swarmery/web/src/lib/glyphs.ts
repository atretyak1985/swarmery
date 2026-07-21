// Shared tool → glyph mapping (Timeline rows + the detail-rail call tree).

export const TOOL_GLYPHS: Record<string, string> = {
  Read: '▤',
  Grep: '⌕',
  Glob: '⌕',
  Edit: '✎',
  Write: '✎',
  Bash: '❯',
  Agent: '⬡',
  Task: '⬡',
  Skill: '◈',
  WebFetch: '↯',
  WebSearch: '↯',
};

export function toolGlyph(name: string): string {
  return TOOL_GLYPHS[name] ?? '·';
}
