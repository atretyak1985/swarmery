// Unit tests for the Docs section's pure rules: the status badges of a parsed
// `# How to use` guide, the section a panel opens on, the missing-subsection
// notice, the file a guide belongs in, and the SectionTabs keyboard reducer.
// Pure logic, no DOM.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/system/docsSection.test.ts`.

import { describe, expect, it } from 'vitest';
import type { SystemDocs } from '../../api/types';
import {
  defaultSection,
  docsStatusTone,
  guidePath,
  missingLabel,
  nextTabIndex,
  tabIndexOf,
} from './docsSection';

/** The wire shape of an item with no guide — what most of the corpus serves
 * today. `sections`/`missing` are [] and never null, per the DTO. */
const NO_DOCS: SystemDocs = {
  present: false,
  duplicate: false,
  markdown: '',
  sections: [],
  missing: [],
  status: '',
  stale: false,
};

function docs(patch: Partial<SystemDocs>): SystemDocs {
  return { ...NO_DOCS, present: true, markdown: '# How to use\n', ...patch };
}

const labels = (d: SystemDocs): string[] => docsStatusTone(d).map((b) => b.label);

describe('docsStatusTone', () => {
  it('gives a reviewed guide one green badge', () => {
    const [badge, ...rest] = docsStatusTone(docs({ status: 'reviewed' }));
    expect(badge?.label).toBe('reviewed');
    expect(badge?.tone).toContain('green');
    expect(rest).toHaveLength(0);
  });

  it('gives a generated guide one amber badge', () => {
    const [badge] = docsStatusTone(docs({ status: 'generated' }));
    expect(badge?.label).toBe('generated');
    expect(badge?.tone).toContain('amber');
  });

  it('reports a stale guide in amber, with the reason in the tip', () => {
    const [badge] = docsStatusTone(docs({ stale: true }));
    expect(badge?.label).toBe('stale');
    expect(badge?.tone).toContain('amber');
    expect(badge?.tip).toContain('source_sha');
  });

  it('reports a duplicate block in red', () => {
    const [badge] = docsStatusTone(docs({ duplicate: true }));
    expect(badge?.label).toBe('two How-to-use blocks');
    expect(badge?.tone).toContain('red');
  });

  // The four states are not mutually exclusive on the wire, so collapsing them
  // to one pill would hide a true finding.
  it('keeps every applicable badge when the states overlap', () => {
    expect(labels(docs({ status: 'reviewed', stale: true, duplicate: true }))).toEqual([
      'reviewed',
      'stale',
      'two How-to-use blocks',
    ]);
  });

  it('surfaces an unrecognised docs.status verbatim instead of normalising it', () => {
    const [badge] = docsStatusTone(docs({ status: 'draft' }));
    expect(badge?.label).toBe('status: draft');
    expect(badge?.tone).toContain('amber');
  });

  it('badges nothing when there is no guide — the empty state says it all', () => {
    expect(docsStatusTone(NO_DOCS)).toEqual([]);
  });

  it('badges nothing for a present guide with no status and no problems', () => {
    expect(docsStatusTone(docs({}))).toEqual([]);
  });
});

describe('defaultSection', () => {
  it('opens on the guide when one exists', () => {
    expect(defaultSection(docs({ status: 'reviewed' }))).toBe('docs');
  });

  it('opens on the definition rather than onto an empty state', () => {
    expect(defaultSection(NO_DOCS)).toBe('definition');
  });
});

describe('missingLabel', () => {
  it('says nothing when the guide is complete', () => {
    expect(missingLabel([])).toBeNull();
  });

  it('names a single missing subsection', () => {
    expect(missingLabel(['Worked example'])).toBe('this guide is missing: Worked example');
  });

  it('names several in contract order', () => {
    expect(missingLabel(['What it does', 'How to invoke', 'Worked example'])).toBe(
      'this guide is missing: What it does, How to invoke, Worked example',
    );
  });
});

describe('guidePath', () => {
  it('leaves an agent file path alone', () => {
    expect(guidePath('~/.claude/agents/tech-lead.md', 'agents')).toBe(
      '~/.claude/agents/tech-lead.md',
    );
  });

  it('leaves a command file path alone', () => {
    expect(guidePath('~/.claude/commands/search.md', 'commands')).toBe(
      '~/.claude/commands/search.md',
    );
  });

  // Skills are registered by DIRECTORY (skills.dir_path), so the empty state
  // must name a file the reader can actually open.
  it('points a skill directory at its SKILL.md', () => {
    expect(guidePath('~/.claude/skills/testing', 'skills')).toBe('~/.claude/skills/testing/SKILL.md');
  });

  it('tolerates a trailing slash on a skill directory', () => {
    expect(guidePath('~/.claude/skills/testing/', 'skills')).toBe(
      '~/.claude/skills/testing/SKILL.md',
    );
  });

  it('does not double-append when a skill path is already a file', () => {
    expect(guidePath('~/.claude/skills/testing/SKILL.md', 'skills')).toBe(
      '~/.claude/skills/testing/SKILL.md',
    );
  });
});

describe('nextTabIndex', () => {
  it('moves right', () => {
    expect(nextTabIndex('ArrowRight', 0, 2)).toBe(1);
  });

  it('moves left', () => {
    expect(nextTabIndex('ArrowLeft', 1, 2)).toBe(0);
  });

  it('wraps around going right off the end', () => {
    expect(nextTabIndex('ArrowRight', 1, 2)).toBe(0);
    expect(nextTabIndex('ArrowRight', 2, 3)).toBe(0);
  });

  it('wraps around going left off the start', () => {
    expect(nextTabIndex('ArrowLeft', 0, 2)).toBe(1);
    expect(nextTabIndex('ArrowLeft', 0, 3)).toBe(2);
  });

  it('jumps to the ends with Home and End', () => {
    expect(nextTabIndex('Home', 2, 3)).toBe(0);
    expect(nextTabIndex('End', 0, 3)).toBe(2);
  });

  it('returns null for keys it does not own, so the event is left alone', () => {
    expect(nextTabIndex('ArrowUp', 0, 2)).toBeNull();
    expect(nextTabIndex('a', 0, 2)).toBeNull();
    expect(nextTabIndex('Enter', 0, 2)).toBeNull();
  });

  it('returns null for an empty tablist', () => {
    expect(nextTabIndex('ArrowRight', 0, 0)).toBeNull();
  });

  it('treats an unknown active tab as the first one', () => {
    expect(nextTabIndex('ArrowRight', -1, 2)).toBe(1);
    expect(nextTabIndex('ArrowLeft', -1, 2)).toBe(1);
  });
});

describe('tabIndexOf (roving tabindex)', () => {
  const tabs = ['docs', 'definition'];

  it('puts exactly one tab of a tablist in the tab order', () => {
    for (const active of tabs) {
      const tabbable = tabs.filter((t) => tabIndexOf(t, active, tabs) === 0);
      expect(tabbable).toEqual([active]);
    }
  });

  it('keeps the inactive tabs out of the tab order', () => {
    expect(tabIndexOf('definition', 'docs', tabs)).toBe(-1);
    expect(tabIndexOf('docs', 'docs', tabs)).toBe(0);
  });

  // The state the roving tabindex used to strand: an active value the tablist
  // does not contain (a hand-edited ?sec=, or a panel rendered with a section
  // it dropped). Every tab returning -1 removes the whole tablist from the tab
  // order, so it can never be focused and the arrow keys never get a chance.
  it('falls back to the first tab when active is not in the list', () => {
    const tabbable = tabs.filter((t) => tabIndexOf(t, 'nonsense', tabs) === 0);
    expect(tabbable).toEqual(['docs']);
  });

  it('leaves at least one tabbable tab for every active value, in or out of the list', () => {
    for (const active of [...tabs, '', 'nonsense', 'DOCS']) {
      const tabbable = tabs.filter((t) => tabIndexOf(t, active, tabs) === 0);
      expect(tabbable).toHaveLength(1);
    }
  });

  it('has nothing to make tabbable in an empty tablist', () => {
    expect(tabIndexOf('docs', 'docs', [])).toBe(-1);
  });
});
