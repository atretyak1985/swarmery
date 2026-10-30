// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '../../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, PhaseSurprise } from '../../api/types';
import { ForecastStory } from './ForecastStory';
import { PhaseCard } from './PhaseCard';
import { PhasePanel, type PhasePanelProps, phasePanelSubtitle } from './PhasePanel';

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
const epic = { taskId: 7, title: 'Bids', phases: [phase(1), scored, phase(3)] } as unknown as Epic;

function renderPanel(over: Partial<PhasePanelProps> = {}) {
  const props: PhasePanelProps = {
    epic,
    phase: scored,
    tab: 'story',
    onTab: vi.fn(),
    onPrev: vi.fn(),
    onNext: vi.fn(),
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
      <PhasePanel {...props} />
    </MemoryRouter>,
  );
  return props;
}

describe('PhasePanel', () => {
  it('is a region titled by the phase (not a dialog), and says where the phase stands', () => {
    renderPanel();
    expect(screen.getByRole('region', { name: 'API: bidding mode, journal' })).toBeTruthy();
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(phasePanelSubtitle(epic, scored)).toBe('Phase 2 of 3 · 0/8 criteria');
    expect(screen.getByText('phase 2 of 3')).toBeTruthy();
  });

  it('the breadcrumb goes back to the plan', () => {
    const p = renderPanel({ onBack: vi.fn() });
    fireEvent.click(screen.getByRole('button', { name: /Bids/ }));
    expect(p.onBack).toHaveBeenCalledTimes(1);
  });

  it('takes focus on open so a keyboard user lands on the content', () => {
    renderPanel();
    expect(document.activeElement).toBe(screen.getByRole('region', { name: 'API: bidding mode, journal' }));
  });

  it('ArrowUp / ArrowDown step to the previous / next phase', () => {
    const p = renderPanel();
    fireEvent.keyDown(document, { key: 'ArrowDown' });
    fireEvent.keyDown(document, { key: 'ArrowUp' });
    expect(p.onNext).toHaveBeenCalledTimes(1);
    expect(p.onPrev).toHaveBeenCalledTimes(1);
  });

  it('the arrow buttons step too, and stand down at the ends', () => {
    const p = renderPanel({ onPrev: undefined });
    fireEvent.click(screen.getByRole('button', { name: 'next phase' }));
    expect(p.onNext).toHaveBeenCalledTimes(1);
    expect((screen.getByRole('button', { name: 'previous phase' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('the arrows leave a text field alone', () => {
    const p = renderPanel({ children: <textarea aria-label="doc" /> });
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'doc' }), { key: 'ArrowDown' });
    expect(p.onNext).not.toHaveBeenCalled();
  });

  it('shows the six tabs with their counts and switches them', () => {
    const p = renderPanel({ tabCounts: { criteria: '0/8' } });
    const tabs = screen.getAllByRole('tab').map((t) => t.textContent);
    expect(tabs).toEqual(['Story', 'Plain words', 'Criteria0/8', 'Runs', 'Review', 'Report']);
    expect(screen.getByRole('tab', { name: 'Story' }).getAttribute('aria-selected')).toBe('true');
    fireEvent.click(screen.getByRole('tab', { name: /Criteria/ }));
    expect(p.onTab).toHaveBeenCalledWith('criteria');
  });

  it('puts the actions on the tabs line; Edit is one of them, not a tab', () => {
    renderPanel({ actions: <button type="button">edit</button> });
    expect(screen.queryByRole('tab', { name: 'Edit' })).toBeNull();
    expect(screen.getByRole('button', { name: 'edit' })).toBeTruthy();
  });

  it('Story renders the phase card and the forecast story, breakdown folded', () => {
    renderPanel();
    expect(screen.getByTestId('phase-card')).toBeTruthy();
    const story = screen.getByTestId('forecast-story');
    expect(story.textContent).toContain('what follows');
    const fold = screen.getByText('show the score breakdown').closest('details');
    expect(fold).not.toBeNull();
    expect(fold?.open).toBe(false);
    expect(screen.getByRole('button', { name: 'Retry run' })).toBeTruthy();
  });
});
