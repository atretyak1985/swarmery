// @vitest-environment jsdom
//
// The phase card's landing chip (Phase 5, SC-11): one chip per landing state,
// labelled from the provider `terms` only, a link to an open change request,
// and nothing at all before the landing flow or before the terms load.

import { cleanup, render, screen } from '../../test/render';
import { afterEach, describe, expect, it } from 'vitest';
import type { EpicPhase, PhaseLanding, ProviderTerms } from '../../api/types';
import { PhaseCard } from './PhaseCard';

afterEach(cleanup);

// Two neutral vocabularies: the card may only ever read them.
const PR_TERMS: ProviderTerms = { provider: 'Host A', change: 'Pull Request', changeShort: 'PR' };
const MR_TERMS: ProviderTerms = { provider: 'Host B', change: 'Merge Request', changeShort: 'MR' };

const landing = (over: Partial<PhaseLanding> = {}): PhaseLanding => ({
  state: 'none',
  prUrl: null,
  prNumber: null,
  prProvider: null,
  prStatus: null,
  landedAt: null,
  error: null,
  ...over,
});

function phase(over: Partial<EpicPhase> = {}): EpicPhase {
  return {
    id: 12,
    seq: 2,
    name: 'Line-item CRUD',
    checkboxesDone: 4,
    checkboxesTotal: 4,
    runCheckboxesBefore: 0,
    runOutcome: 'completed',
    runState: 'done',
    runError: null,
    runModel: null,
    docModel: null,
    runStartedAt: null,
    runEndedAt: null,
    docUpdatedAt: null,
    completionReport: null,
    forecasts: [],
    forecastLints: [],
    surprise: null,
    landing: landing(),
    ...over,
  } as unknown as EpicPhase;
}

const chip = (): HTMLElement | null => screen.queryByTestId('phase-landing-chip');

describe('PhaseCard landing chip', () => {
  it('shows nothing before the landing flow', () => {
    render(<PhaseCard phase={phase()} terms={PR_TERMS} />);
    expect(chip()).toBeNull();
  });

  it.each([
    ['ready', 'ready to land'],
    ['pushed', 'pushed'],
    ['merged', 'merged ✓'],
    ['returned', 'returned to agent'],
  ] as const)('labels %s as "%s"', (state, text) => {
    render(<PhaseCard phase={phase({ landing: landing({ state, landedAt: '2026-10-09T10:00:00Z' }) })} terms={PR_TERMS} />);
    const el = chip();
    expect(el?.textContent).toBe(text);
    expect(el?.tagName).toBe('SPAN');
  });

  it('dates only a merged chip, from landedAt (the merge time)', () => {
    render(<PhaseCard phase={phase({ landing: landing({ state: 'merged', landedAt: '2026-10-09T10:00:00Z' }) })} terms={PR_TERMS} />);
    expect(chip()?.getAttribute('title')).toMatch(/^merged /);
    cleanup();

    // A landedAt left on a non-merged phase (stamped at push by an older daemon) is not shown.
    render(<PhaseCard phase={phase({ landing: landing({ state: 'pushed', landedAt: '2026-10-09T10:00:00Z' }) })} terms={PR_TERMS} />);
    expect(chip()?.getAttribute('title')).toBeNull();
    cleanup();

    render(<PhaseCard phase={phase({ landing: landing({ state: 'merged' }) })} terms={PR_TERMS} />);
    expect(chip()?.textContent).toBe('merged ✓');
    expect(chip()?.getAttribute('title')).toBeNull();
  });

  it('links an open change request in a new tab, labelled from terms', () => {
    const open = landing({ state: 'pr_open', prUrl: 'https://host.example/acme/w/12', prNumber: 12 });
    render(<PhaseCard phase={phase({ landing: open })} terms={PR_TERMS} />);
    const link = screen.getByRole('link', { name: 'PR #12' });
    expect(link.getAttribute('href')).toBe('https://host.example/acme/w/12');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(link.getAttribute('rel')).toContain('noreferrer');
    cleanup();

    render(<PhaseCard phase={phase({ landing: open })} terms={MR_TERMS} />);
    expect(screen.getByRole('link', { name: 'MR #12' })).toBeTruthy();
  });

  it('stays hidden until the terms load, and on a phase without a landing', () => {
    render(<PhaseCard phase={phase({ landing: landing({ state: 'ready' }) })} terms={null} />);
    expect(chip()).toBeNull();
    cleanup();

    render(<PhaseCard phase={phase({ landing: undefined as unknown as PhaseLanding })} terms={PR_TERMS} />);
    expect(chip()).toBeNull();
  });
});

// Decision ids (migration 0122): a phase whose decision is undecided and
// expensive shows "waits on D-n" inline, linking to the planning turn.
describe('PhaseCard decision hold', () => {
  const hold = {
    detail: 'decision-undecided: D-3 (Which store?)',
    decisions: [
      { decisionId: 'D-3', question: 'Which store?', seq: 3, planningSessionId: 44 },
      { decisionId: 'D-5', question: 'Split the service?', seq: 5, planningSessionId: null },
    ],
  };

  it('renders nothing when nothing holds the phase', () => {
    render(<PhaseCard phase={phase({ decisionHold: null })} />);
    expect(screen.queryByTestId('decision-hold')).toBeNull();
  });

  it('names each holding decision and links the ones an interview asked', () => {
    render(
      <PhaseCard
        phase={phase({ decisionHold: hold, runOutcome: 'idle', runState: 'idle' })}
        decisionHref={(d) => (d.planningSessionId === null ? null : `/plan?turn=${String(d.seq)}`)}
      />,
    );
    const note = screen.getByTestId('decision-hold');
    expect(note.textContent).toContain('waits on decision D-3');
    expect(note.textContent).toContain('Which store?');
    expect(note.textContent).toContain('waits on decision D-5');
    const links = screen.getAllByRole('link', { name: 'open the question →' });
    expect(links).toHaveLength(1);
    expect(links[0]?.getAttribute('href')).toBe('/plan?turn=3');
  });
});

// verify-not-trust phase 10: the hold prefers the daemon's own planning-turn
// link, and an open agent-declared stop reads "waits on decision D-n" inline,
// with its trigger and reason and a link to where it is answered.
describe('PhaseCard decision links and stops', () => {
  it('links the planning turn the daemon sent, over the caller-built link', () => {
    const hold = {
      detail: 'decision-undecided: D-3 (Which store?)',
      decisions: [
        {
          decisionId: 'D-3',
          question: 'Which store?',
          seq: 3,
          planningSessionId: 44,
          planningTurn: '/p/orders/plans?tab=new&planningSession=44&turn=3#planning-turn-3',
        },
      ],
    };
    render(<PhaseCard phase={phase({ decisionHold: hold })} decisionHref={() => '/fallback'} />);
    const link = screen.getByRole('link', { name: 'open the question →' });
    expect(link.getAttribute('href')).toBe('/p/orders/plans?tab=new&planningSession=44&turn=3#planning-turn-3');
  });

  it('ignores a planning turn that is not a same-origin path', () => {
    const hold = {
      detail: 'decision-undecided: D-3 (Which store?)',
      decisions: [
        { decisionId: 'D-3', question: 'Which store?', seq: 3, planningSessionId: 44, planningTurn: '//evil.example/x' },
      ],
    };
    render(<PhaseCard phase={phase({ decisionHold: hold })} decisionHref={() => '/fallback'} />);
    expect(screen.getByRole('link', { name: 'open the question →' }).getAttribute('href')).toBe('/fallback');
  });

  const stop = {
    taskId: 7,
    phaseId: 12,
    decisionId: 'D-7',
    source: 'stop' as const,
    question: 'The criterion needs pkg/orders/line_items_test.go, which the guard protects.',
    options: [
      { id: 'allow-add-to-scope', label: 'Let me edit the test', consequence: 'the test changes with the code' },
      { id: 'keep-test', label: 'Leave the test alone', consequence: 'the criterion stays open' },
    ],
    guard: {},
    trigger: 'protected-path',
    reason: 'The criterion needs pkg/orders/line_items_test.go, which the guard protects.',
    freeText: true,
  };

  it('shows an open agent stop inline with its trigger, reason and answer link', () => {
    render(<PhaseCard phase={phase({ guardDecision: stop })} stopHref="/p/orders/needs-you" />);
    const note = screen.getByTestId('stop-decision');
    expect(note.textContent).toContain('waits on decision D-7');
    expect(note.textContent).toContain('stop: protected-path');
    expect(note.textContent).toContain(stop.reason);
    expect(screen.getByRole('link', { name: 'answer it →' }).getAttribute('href')).toBe('/p/orders/needs-you');
  });

  it('shows nothing for a plain guard decision or no decision', () => {
    const { trigger: _t, reason: _r, freeText: _f, ...guardOnly } = stop;
    render(<PhaseCard phase={phase({ guardDecision: guardOnly })} />);
    expect(screen.queryByTestId('stop-decision')).toBeNull();
    cleanup();
    render(<PhaseCard phase={phase({ guardDecision: null })} />);
    expect(screen.queryByTestId('stop-decision')).toBeNull();
  });
});
