// @vitest-environment jsdom
//
// The phase card's landing chip (Phase 5, SC-11): one chip per landing state,
// labelled from the provider `terms` only, a link to an open change request,
// and nothing at all before the landing flow or before the terms load.

import { cleanup, render, screen } from '@testing-library/react';
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
