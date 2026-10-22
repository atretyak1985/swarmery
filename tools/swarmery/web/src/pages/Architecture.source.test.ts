// @vitest-environment node
//
// The architecture map is framed sandboxed (opaque origin), so the page must
// never try to reach into the frame's document — postMessage is the only
// channel. A source-level pin, so a reintroduced same-origin path fails here
// even if no rendered test happens to exercise it.

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const read = (rel: string): string => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8');

describe('repository artifact frames', () => {
  it('Architecture never reads the frame document', () => {
    expect(read('./Architecture.tsx')).not.toContain('contentDocument');
  });

  it('both artifact iframes are sandboxed to scripts only', () => {
    for (const file of ['./Architecture.tsx', './Graphify.tsx']) {
      const source = read(file);
      expect(source, file).toContain('sandbox="allow-scripts"');
      expect(source, file).not.toMatch(/sandbox=[^>]*allow-same-origin/);
    }
  });
});
