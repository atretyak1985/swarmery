// The Docs section of a System detail panel: an item's `# How to use` usage
// guide, rendered where the reader meets the item.
//
// Presentational only — the panel is handed an already-parsed guide
// (systemDocsDTO) and renders it with lib/markdown, the SAME dependency-free
// renderer Docs.tsx and the editor preview use. No markdown dependency is added
// by this file, by design.
//
// The empty state is not an afterthought: the corpus is currently undocumented
// end to end, so "no guide yet" is what most items show today. It therefore
// names the exact file to edit and the heading to add, instead of the usual
// blank shrug.
//
// The pure rules (badges, default section, missing notice) live in
// ./docsSection and are unit-tested there; they are re-exported here because
// this panel is their documented home.

import { Trans } from '@lingui/react/macro';
import type { SystemDocs } from '../../api/types';
import { Empty } from '../../components/ui';
import { Markdown } from '../../lib/markdown';
import { defaultSection, docsStatusTone, missingLabel } from './docsSection';

export { defaultSection, docsStatusTone, missingLabel };
export type { DocsBadge, DocsSection } from './docsSection';

/** Where the contract says the guide goes — shown in the empty state. */
const GUIDE_CONTRACT = 'tools/swarmery/docs/system-docs-format.md';

export function DocsPanel({
  docs,
  path,
  name,
}: {
  docs: SystemDocs;
  /** The file the guide belongs in (a skill's SKILL.md, not its directory). */
  path: string;
  name: string;
}): JSX.Element {
  if (!docs.present) {
    return (
      <div className="mt-1">
        <Empty>
          <div>
            <Trans>
              no usage guide yet — <b className="font-semibold text-ink-2">{name}</b> has no{' '}
              <code className="font-mono text-[11.5px] text-ink-2">{'# How to use'}</code> section
              in its source file
            </Trans>
          </div>
          <div className="mt-2 font-mono text-[10px] break-all text-ink-faint">{path}</div>
          <div className="mt-2 text-[11.5px] text-ink-dim">
            <Trans>
              add an H1 <code className="font-mono text-[11px]">{'# How to use'}</code> block at the
              end of that file — four subsections are required:{' '}
              <span className="font-mono text-[11px]">
                What it does · When to use it · How to invoke · Worked example
              </span>
            </Trans>
          </div>
          <div className="mt-1.5 font-mono text-[10px] text-ink-faint">
            <Trans>contract: {GUIDE_CONTRACT}</Trans>
          </div>
        </Empty>
      </div>
    );
  }

  const badges = docsStatusTone(docs);
  const missing = missingLabel(docs.missing);

  return (
    <div className="mt-1">
      {badges.length > 0 && (
        <div className="mb-3 flex flex-wrap items-center gap-1.5">
          {badges.map((b) => (
            <span
              key={b.label}
              className={`rounded-full border px-2 py-px font-mono text-[10px] whitespace-nowrap ${b.tone}`}
              {...(b.tip !== null ? { 'data-tip': b.tip } : {})}
            >
              {b.label}
            </span>
          ))}
        </div>
      )}

      {missing !== null && (
        <div
          className="mb-3 rounded-lg border border-amber/35 bg-amber/5 px-3 py-2 font-mono text-[11px] text-amber"
          role="status"
        >
          ▲ {missing}
        </div>
      )}

      <div className="text-[13px] leading-[1.65] text-ink-2">
        <Markdown text={docs.markdown} />
      </div>
    </div>
  );
}
