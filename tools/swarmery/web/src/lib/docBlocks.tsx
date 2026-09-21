// Doc-surface block components for the hand-rolled markdown renderer
// (markdown.tsx): the three fence dialects it dispatches on — ```mermaid,
// ```stats — and the callout the blockquote branch renders for GitHub-style
// admonitions (> [!NOTE] and friends).
//
// XSS posture. markdown.tsx is XSS-safe by construction: it never builds an
// HTML string. This file holds the single, deliberate exception —
// MermaidView assigns mermaid's own rendered SVG to innerHTML, because
// mermaid has no React renderer and returns a serialized SVG string. That is
// safe here only because:
//   · mermaid runs with securityLevel: 'strict', which routes its output
//     through DOMPurify and disables click handlers / raw HTML labels;
//   · the input is doc markdown from the daemon's own go:embed snapshot
//     (internal/docsfs), not user-typed or network-authored content;
//   · a render failure degrades to the plain <pre> that a fence with any
//     other info string would have produced — never a partial injection.
// Nothing else in this file, or in markdown.tsx, may use innerHTML or
// dangerouslySetInnerHTML.

import { useEffect, useId, useRef, useState, type ReactNode } from 'react';

/* ----- shared code-fence presentation ----- */

/** The exact <pre> classes markdown.tsx has always used for a fenced block.
 *
 * Exported so MermaidBlock's error path degrades to markup byte-identical to
 * a plain fence — the fallback must be indistinguishable from "this was never
 * a diagram", or a broken diagram looks like a broken renderer. */
export const CODE_PRE_CLASS =
  'my-2 overflow-x-auto rounded-lg border border-line bg-surface px-3 py-2.5 font-mono text-[11px] leading-relaxed text-ink-2 first:mt-0 last:mb-0';

/** A fenced code block: the renderer's default for every info string it does
 * not special-case (```go, ```bash, bare ```). */
export function CodeBlock({ code }: { code: string }): JSX.Element {
  return (
    <pre className={CODE_PRE_CLASS}>
      <code>{code}</code>
    </pre>
  );
}

/* ----- ```mermaid ----- */

/** mermaid's built-in theme for a resolved app mode.
 *
 * 'neutral' rather than 'default' for light: 'default' hard-codes mermaid's
 * lavender palette, which fights the cream/amber Canvas surfaces; 'neutral' is
 * greyscale and reads as part of the page in every palette.
 *
 * Pure and exported so the theme decision is unit-testable without a browser —
 * the effect below cannot run under renderToStaticMarkup. */
export function mermaidTheme(mode: string | undefined): 'dark' | 'neutral' {
  return mode === 'light' ? 'neutral' : 'dark';
}

/** The resolved mode the app has published on <html data-mode>. */
function currentMode(): string | undefined {
  return document.documentElement.dataset.mode;
}

/** Presentation half of MermaidBlock, split out so both states are testable
 * as pure views: the error state must equal a plain fence exactly. */
export function MermaidView({
  code,
  error,
  hostRef,
}: {
  code: string;
  error: string | null;
  hostRef?: React.RefObject<HTMLDivElement | null>;
}): JSX.Element {
  if (error !== null) return <CodeBlock code={code} />;
  return (
    <div
      ref={hostRef}
      role="img"
      aria-label="diagram"
      className="my-3 overflow-x-auto rounded-lg border border-line bg-surface p-3 first:mt-0 last:mb-0 [&_svg]:mx-auto [&_svg]:h-auto [&_svg]:max-w-full"
    />
  );
}

/** A ```mermaid fence.
 *
 * mermaid is imported dynamically so it never enters the main bundle — the
 * chunk is fetched the first time a doc containing a diagram is opened, and
 * never for the rest of the app.
 *
 * Re-renders on theme change: the diagram's colors are baked into the SVG at
 * render time, so switching light/dark (or palette) must re-run mermaid. The
 * app publishes the resolved mode on <html data-mode>, so a MutationObserver
 * on that attribute is both provider-independent (this component is used by a
 * renderer mounted in several trees) and correct for palette swaps too. */
export function MermaidBlock({ code }: { code: string }): JSX.Element {
  const host = useRef<HTMLDivElement>(null);
  const [error, setError] = useState<string | null>(null);
  // A generation counter, NOT the mode string, is what re-runs the render
  // effect. Deriving state from the attribute instead (setMode(currentMode()))
  // silently fails for a palette-only swap: data-palette changes, data-mode
  // does not, so setState gets the identical string, React bails out on
  // Object.is, and the diagram keeps the previous palette's baked-in colors
  // while the rest of the UI re-tints. Counting mutations has no such
  // equal-value bailout, and the effect reads the live mode itself.
  const [gen, setGen] = useState(0);
  // useId is stable across re-renders but contains ':' — illegal in the CSS
  // selectors mermaid derives from the id, so strip to word characters.
  const baseId = `mmd-${useId().replace(/[^a-zA-Z0-9]/g, '')}`;

  useEffect(() => {
    const obs = new MutationObserver(() => {
      setGen((g) => g + 1);
    });
    obs.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-mode', 'data-palette'],
    });
    return () => {
      obs.disconnect();
    };
  }, []);

  useEffect(() => {
    let alive = true;
    setError(null);
    // Unique per render pass: mermaid.render mounts throwaway scaffolding
    // under this id, so two overlapping passes (a theme flip mid-render)
    // must not share one.
    const domId = `${baseId}-${String(gen)}`;
    void import('mermaid')
      .then(async ({ default: mermaid }) => {
        mermaid.initialize({
          startOnLoad: false,
          theme: mermaidTheme(currentMode()),
          securityLevel: 'strict',
          fontFamily: 'Inter, ui-sans-serif, system-ui, sans-serif',
          // PINNED FOR THE v12 UPGRADE. v12 is a deliberately breaking release:
          // "existing flowcharts, state and class diagrams will re-lay out and
          // recolour" unless the old defaults are named explicitly. Every
          // diagram in the docs surface was authored against them, so they are
          // stated here rather than inherited — an upgrade should not silently
          // redraw content nobody re-reviewed.
          //
          // The release note also says to pin `theme: 'default'`; that advice
          // does NOT apply to this app, which picks 'neutral'/'dark' on purpose
          // (see mermaidTheme above — 'default' hard-codes a lavender palette
          // that fights the Canvas surfaces). Both names still resolve in v12.
          //
          // Drop these two the day someone decides the new look is wanted, and
          // look at the diagrams when they do.
          layout: 'dagre',
          look: 'classic',
        });
        const { svg } = await mermaid.render(domId, code);
        if (!alive) return;
        // See the XSS posture note at the top of this file: mermaid's own
        // DOMPurify-sanitised SVG, the one justified innerHTML in the docs
        // surface.
        if (host.current !== null) host.current.innerHTML = svg;
      })
      .catch((e: unknown) => {
        if (alive) setError(String(e));
      });
    return () => {
      alive = false;
    };
  }, [code, gen, baseId]);

  return <MermaidView code={code} error={error} hostRef={host} />;
}

/* ----- ```stats ----- */

/** One `value | label` line of a ```stats fence, optionally `| hot`. */
export interface StatRow {
  value: string;
  label: string;
  hot: boolean;
}

/** Parse a ```stats body. Blank lines and value-less rows are dropped so a
 * stray trailing newline cannot render an empty tile. Exported for tests. */
export function parseStats(source: string): StatRow[] {
  return source
    .split('\n')
    .map((line) => line.split('|').map((cell) => cell.trim()))
    .filter((cells) => (cells[0] ?? '') !== '')
    .map((cells) => ({
      value: cells[0] ?? '',
      label: cells[1] ?? '',
      hot: (cells[2] ?? '').toLowerCase() === 'hot',
    }));
}

/** A ```stats fence: a strip of number-over-label tiles. `| hot` accents the
 * numeral in the palette's brand hue for the one figure that carries the
 * point of the paragraph. */
export function StatsStrip({ source }: { source: string }): JSX.Element {
  const rows = parseStats(source);
  return (
    <div className="my-3 flex flex-wrap gap-3 first:mt-0 last:mb-0">
      {rows.map((row, idx) => (
        <div
          key={idx}
          className="min-w-[140px] flex-1 rounded-lg border border-line bg-surface px-4 py-3"
        >
          <div
            className={`font-display text-[24px] leading-tight font-medium tabular-nums ${row.hot ? 'text-brand' : 'text-ink'}`}
          >
            {row.value}
          </div>
          <div className="mt-0.5 text-[11px] leading-snug text-ink-dim">{row.label}</div>
        </div>
      ))}
    </div>
  );
}

/* ----- > [!NOTE] admonitions ----- */

/** The four GitHub admonition kinds, mapped to this app's semantic tokens.
 *
 * Token names are the ones index.css actually declares (green/amber/red/blue
 * + brand) — there is no --color-ok/--color-warn in this theme. */
const CALLOUT: Record<string, { label: string; border: string; text: string }> = {
  note: { label: 'Note', border: 'border-blue bg-blue/8', text: 'text-blue' },
  tip: { label: 'Tip', border: 'border-green bg-green/8', text: 'text-green' },
  warning: { label: 'Warning', border: 'border-amber bg-amber/8', text: 'text-amber' },
  important: { label: 'Important', border: 'border-brand bg-brand/8', text: 'text-brand' },
};

/** The admonition kind a blockquote's first line declares, or null for a
 * plain quote. Accepts GitHub's exact syntax only: `[!NOTE]` alone on the
 * line, case-insensitive. Pure + exported for tests. */
export function calloutType(firstLine: string): string | null {
  const m = /^\[!(NOTE|TIP|WARNING|IMPORTANT)\]\s*$/i.exec(firstLine.trim());
  return m === null ? null : (m[1] ?? 'NOTE').toLowerCase();
}

/** A styled callout. Body arrives as already-rendered inline nodes so this
 * file never imports markdown.tsx — which imports this one. */
export function Callout({ type, children }: { type: string; children: ReactNode }): JSX.Element {
  const spec = CALLOUT[type] ?? CALLOUT.note;
  return (
    <div
      className={`my-3 rounded-r-lg border-l-2 py-2.5 pr-3 pl-3 first:mt-0 last:mb-0 ${spec?.border ?? 'border-blue bg-blue/8'}`}
    >
      <div
        className={`mb-1 font-mono text-[10.5px] font-medium tracking-[0.06em] uppercase ${spec?.text ?? 'text-blue'}`}
      >
        {spec?.label ?? 'Note'}
      </div>
      <div className="text-[13px] leading-relaxed whitespace-pre-line text-ink-2">{children}</div>
    </div>
  );
}
