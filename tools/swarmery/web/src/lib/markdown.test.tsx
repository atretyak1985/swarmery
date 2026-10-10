// Unit tests for the markdown renderer's doc-surface blocks: the ```mermaid /
// ```stats / ```figure fence dialects, blockquotes and GitHub admonitions, and
// the regression that fences with any OTHER info string still render exactly
// the <pre> they always did.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/lib/markdown.test.tsx`.
//
// Rendering goes through react-dom/server (test/render's renderStatic), so no DOM
// environment is needed. Consequence: effects never run, so MermaidBlock's
// dynamic import is out of reach here — its two states are asserted through
// the pure MermaidView instead, and the theme decision through mermaidTheme.
// What only a browser can prove (mermaid actually emitting an SVG) is called
// out in the phase's Completion Report rather than faked here.

import { renderStatic } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { CODE_PRE_CLASS, CodeBlock, MermaidView, calloutType, mermaidTheme, parseStats } from './docBlocks';
import { FIGURE_NAMES } from './docFigures';
import { Markdown } from './markdown';

/** Render markdown the way the app does — inside a router, since links become
 * react-router <Link>s that throw outside one. */
function render(md: string): string {
  return renderStatic(
    <MemoryRouter>
      <Markdown text={md} />
    </MemoryRouter>,
  );
}

describe('```stats fences', () => {
  it('renders one tile per row, with values and labels', () => {
    const html = render(['```stats', '253 | cards total', '248 | stuck in triage', '2 | done ever', '```'].join('\n'));
    expect(html).toContain('253');
    expect(html).toContain('cards total');
    expect(html).toContain('248');
    expect(html).toContain('2');
    expect(html).toContain('done ever');
  });

  it('accents only the row flagged hot', () => {
    const html = render(['```stats', '12 | after the amnesty | hot', '253 | before', '```'].join('\n'));
    // The hot numeral takes the brand hue; the plain one stays ink.
    expect(html).toContain('text-brand');
    expect(html).toContain('text-ink');
  });

  it('parses `value | label [| hot]` and drops blank rows', () => {
    expect(parseStats('7 | seven\n\n3 | three | hot\n')).toEqual([
      { value: '7', label: 'seven', hot: false },
      { value: '3', label: 'three', hot: true },
    ]);
  });
});

describe('blockquotes and admonitions', () => {
  it('renders a plain quote as a <blockquote>', () => {
    const html = render('> just a quote');
    expect(html).toContain('<blockquote');
    expect(html).toContain('just a quote');
    expect(html).not.toContain('Warning');
  });

  it('renders [!WARNING] as a labelled callout, not a blockquote', () => {
    const html = render('> [!WARNING]\n> mind the gap');
    expect(html).toContain('Warning');
    expect(html).toContain('mind the gap');
    expect(html).not.toContain('<blockquote');
    expect(html).not.toContain('[!WARNING]');
  });

  it('labels each admonition kind', () => {
    expect(render('> [!NOTE]\n> x')).toContain('Note');
    expect(render('> [!TIP]\n> x')).toContain('Tip');
    expect(render('> [!IMPORTANT]\n> x')).toContain('Important');
  });

  it('detects the admonition kind case-insensitively, and only when alone on the line', () => {
    expect(calloutType('[!NOTE]')).toBe('note');
    expect(calloutType('[!warning]')).toBe('warning');
    expect(calloutType('[!NOTE] trailing prose')).toBeNull();
    expect(calloutType('plain quote')).toBeNull();
  });

  it('renders inline markup inside a callout body', () => {
    expect(render('> [!TIP]\n> use **bold** here')).toContain('<strong');
  });
});

describe('```figure fences', () => {
  it('renders a registered figure', () => {
    const html = render('```figure board-lanes\n```');
    expect(html).toContain('Inbox');
    expect(html).toContain('Review');
  });

  it('renders every registered name without throwing', () => {
    expect(FIGURE_NAMES.length).toBeGreaterThanOrEqual(4);
    for (const name of FIGURE_NAMES) {
      expect(render('```figure ' + name + '\n```').length).toBeGreaterThan(0);
    }
  });

  it('renders an inline notice for an unknown name', () => {
    expect(render('```figure nope\n```')).toContain('unknown figure: nope');
  });
});

describe('```mermaid fences', () => {
  it('picks a theme from the resolved app mode', () => {
    expect(mermaidTheme('light')).toBe('neutral');
    expect(mermaidTheme('dark')).toBe('dark');
    expect(mermaidTheme(undefined)).toBe('dark');
  });

  it('degrades a failed diagram to markup identical to a plain code fence', () => {
    const code = 'flowchart LR\n  A --> B';
    const failed = renderStatic(<MermaidView code={code} error="boom" />);
    const plain = renderStatic(<CodeBlock code={code} />);
    expect(failed).toBe(plain);
  });

  it('renders a host container, not the source, while healthy', () => {
    const html = renderStatic(<MermaidView code={'flowchart LR\n  A --> B'} error={null} />);
    expect(html).toContain('role="img"');
    expect(html).not.toContain('flowchart');
  });
});

describe('regression: untouched syntax', () => {
  it('renders a ```go fence as the same <pre> as before, with no info-string leak', () => {
    const html = render('```go\nfunc main() {}\n```');
    expect(html).toBe(`<pre class="${CODE_PRE_CLASS}"><code>func main() {}</code></pre>`);
    expect(html).not.toContain('go<');
  });

  it('renders a bare fence identically', () => {
    const html = render('```\nplain\n```');
    expect(html).toBe(`<pre class="${CODE_PRE_CLASS}"><code>plain</code></pre>`);
  });

  it('still renders headings, lists, tables and links', () => {
    const html = render(
      ['## Heading', '', '- one', '- two', '', '| a | b |', '|---|---|', '| 1 | 2 |', '', 'see [docs](CONCEPTS.md)'].join('\n'),
    );
    expect(html).toContain('<h3');
    expect(html).toContain('Heading');
    expect(html).toContain('<ul');
    expect(html).toContain('<li>one</li>');
    expect(html).toContain('<table');
    // Attribute order is React's, not the source's — assert the href alone.
    expect(html).toContain('href="/docs/concepts"');
  });

  it('never emits a raw HTML string from doc source', () => {
    const html = render('<img src=x onerror=alert(1)>\n\n> [!NOTE]\n> <b>not bold</b>');
    expect(html).not.toContain('<img');
    expect(html).not.toContain('<b>not bold</b>');
    expect(html).toContain('&lt;b&gt;not bold&lt;/b&gt;');
  });
});
