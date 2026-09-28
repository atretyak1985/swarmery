// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, PhaseSurprise } from '../../api/types';
import { ForecastStory } from './ForecastStory';
import { PhaseCard } from './PhaseCard';
import { PhaseDrawer, type PhaseDrawerProps, phaseDrawerSubtitle } from './PhaseDrawer';

afterEach(cleanup);

const SURPRISE = {
  index: 0.72,
  top: 'size_miss',
  components: { size_miss: 1, duration_miss: 1 },
  weights: { size_miss: 0.15, duration_miss: 0.1 },
  detail: {
    forecastKind: 'posterior',
    forecastPostHoc: false,
    forecastAreas: ['apps/api/src/modules/bids'],
    actualAreas: [],
    unexpectedAreas: [],
    missedAreas: [],
    matchedAreas: [],
    forecastSize: 'L',
    actualSize: 'XS',
    sizeDistance: 3,
    forecastDuration: '90m-4h',
    actualDuration: '<30m',
    actualDurationS: 30,
    durationDistance: 2,
    forecastOutcome: 'done_with_concerns',
    actualOutcome: 'noop',
    testFailuresUnexpected: null,
    confidence: 0.55,
    majorMiss: true,
  },
  revision: null,
  summary: '',
  phaseId: 12,
  sessionUuid: 's-1',
  forecastDocHash: '',
  actualsSource: 'transcript',
  notifiedAt: null,
  autoVerifyAt: null,
  computedAt: '2026-09-27T10:00:00Z',
} as unknown as PhaseSurprise;

function phase(seq: number, over: Partial<EpicPhase> = {}): EpicPhase {
  return {
    id: 10 + seq,
    seq,
    name: `Phase ${String(seq)} name`,
    checkboxesDone: 0,
    checkboxesTotal: 8,
    runCheckboxesBefore: 0,
    runOutcome: 'noop',
    runState: 'idle',
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
    ...over,
  } as unknown as EpicPhase;
}

const scored = phase(2, { name: 'API: bidding mode, journal', surprise: SURPRISE });
const epic = { taskId: 7, phases: [phase(1), scored, phase(3)] } as unknown as Epic;

function renderDrawer(over: Partial<PhaseDrawerProps> = {}) {
  const props: PhaseDrawerProps = {
    epic,
    phase: scored,
    tab: 'story',
    onTab: vi.fn(),
    onPrev: vi.fn(),
    onNext: vi.fn(),
    onClose: vi.fn(),
    children: (
      <>
        <PhaseCard phase={scored} primary={{ label: 'Retry run', onClick: vi.fn() }} />
        <ForecastStory phase={scored} />
      </>
    ),
    ...over,
  };
  render(
    <MemoryRouter>
      <PhaseDrawer {...props} />
    </MemoryRouter>,
  );
  return props;
}

describe('PhaseDrawer', () => {
  it('is a dialog titled by the phase, with the "Phase n of m" subtitle', () => {
    renderDrawer();
    expect(screen.getByRole('dialog', { name: 'API: bidding mode, journal' })).toBeTruthy();
    expect(phaseDrawerSubtitle(epic, scored)).toBe('Phase 2 of 3 · 0/8 criteria');
    expect(screen.getByText('Phase 2 of 3 · 0/8 criteria')).toBeTruthy();
  });

  it('Esc closes', () => {
    const p = renderDrawer();
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(p.onClose).toHaveBeenCalledTimes(1);
  });

  it('ArrowUp / ArrowDown step to the previous / next phase', () => {
    const p = renderDrawer();
    fireEvent.keyDown(document, { key: 'ArrowDown' });
    fireEvent.keyDown(document, { key: 'ArrowUp' });
    expect(p.onNext).toHaveBeenCalledTimes(1);
    expect(p.onPrev).toHaveBeenCalledTimes(1);
  });

  it('shows Story · Criteria · Runs · Report · Edit and switches tabs', () => {
    const p = renderDrawer();
    const tabs = screen.getAllByRole('tab').map((t) => t.textContent);
    expect(tabs).toEqual(['Story', 'Criteria0/8', 'Runs', 'Report', 'Edit']);
    expect(screen.getByRole('tab', { name: 'Story' }).getAttribute('aria-selected')).toBe('true');
    fireEvent.click(screen.getByRole('tab', { name: /Criteria/ }));
    expect(p.onTab).toHaveBeenCalledWith('criteria');
  });

  it('retires the Edit tab on a done phase', () => {
    renderDrawer({ editable: false });
    expect(screen.queryByRole('tab', { name: 'Edit' })).toBeNull();
  });

  it('Story renders the phase card and the forecast story, breakdown folded', () => {
    renderDrawer();
    expect(screen.getByTestId('phase-card')).toBeTruthy();
    const story = screen.getByTestId('forecast-story');
    expect(story.textContent).toContain('what follows');
    const fold = screen.getByText('show the score breakdown').closest('details');
    expect(fold).not.toBeNull();
    expect(fold?.open).toBe(false);
    expect(screen.getByRole('button', { name: 'Retry run' })).toBeTruthy();
  });
});
