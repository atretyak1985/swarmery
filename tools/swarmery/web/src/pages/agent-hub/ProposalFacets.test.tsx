// @vitest-environment jsdom
//
// Proposal facets (harness² phase 3). The claims:
//
//   1. Form and lens chips name the rung and the lens; an older row (no form,
//      no lens) renders nothing.
//   2. A KEEP_BASE row shows "kept as is" in place of the status chip — never
//      the word "rejected" — and its reason under its own heading.
//   3. Follow-ups list each form and text; warnings list each warning.
//   4. The labels are translated in uk.
//
// `npx vitest run src/pages/agent-hub/ProposalFacets.test.tsx`.

import { afterEach, describe, expect, it } from 'vitest';
import type { AgentChangeProposal } from '../../api/types';
import { activate } from '../../i18n';
import { act, cleanup, render, screen, within } from '../../test/render';
import { isKeptBase, KeptBaseChip, ProposalFacets, ProposalFormLensChips } from './ProposalFacets';

afterEach(async () => {
  cleanup();
  await act(() => activate('en'));
});

function proposal(over: Partial<AgentChangeProposal> = {}): AgentChangeProposal {
  return {
    id: 1,
    recommendation_id: null,
    agent: 'tech-lead',
    agent_path: 'plugins/core/agents/tech-lead.md',
    target_kind: 'agent',
    target_path: '',
    base_sha256: 'x',
    diff: '',
    rationale: 'because',
    status: 'proposed',
    error: null,
    pr_url: null,
    created_at: '2026-11-02T00:00:00.000Z',
    decided_at: null,
    ...over,
  };
}

const keptBase = proposal({
  status: 'rejected',
  outcome: 'kept_base',
  form: 'keep_base',
  rationale: 'Line 12 already requires the exit-code check.',
  suggested_followups: [{ form: 'script', text: 'make the runner propagate the exit code' }],
});

describe('ProposalFormLensChips', () => {
  it('names the form and the lens', () => {
    render(<ProposalFormLensChips p={proposal({ form: 'always-on', lens: 'mixed' })} />);
    expect(screen.getByTestId('proposal-form').textContent).toBe('always-on text');
    expect(screen.getByTestId('proposal-lens').textContent).toBe('procedure + knowledge');
  });

  it('renders nothing for a row from before the lens existed', () => {
    const { container } = render(<ProposalFormLensChips p={proposal()} />);
    expect(container.textContent).toBe('');
  });

  it('shows no form chip for KEEP_BASE (the kept-as-is chip says it)', () => {
    render(<ProposalFormLensChips p={keptBase} />);
    expect(screen.queryByTestId('proposal-form')).toBeNull();
  });
});

describe('KEEP_BASE', () => {
  it('is told apart from a human rejection', () => {
    expect(isKeptBase(keptBase)).toBe(true);
    expect(isKeptBase(proposal({ status: 'rejected' }))).toBe(false);
    expect(isKeptBase(proposal({ status: 'rejected', outcome: '' }))).toBe(false);

    render(<KeptBaseChip />);
    const chip = screen.getByTestId('proposal-kept-base');
    expect(chip.textContent).toBe('kept as is');
    expect(chip.textContent).not.toMatch(/reject/i);
    expect(chip.getAttribute('data-tip')).toMatch(/Nobody rejected it/);
  });

  it('shows the reason under its own heading, with the follow-ups', () => {
    render(<ProposalFacets p={keptBase} />);
    const reason = screen.getByTestId('proposal-keep-base-reason');
    expect(reason.textContent).toContain('Why the file stays as it is');
    expect(reason.textContent).toContain('Line 12 already requires the exit-code check.');
    const list = screen.getByRole('list', { name: 'follow-ups' });
    expect(within(list).getByText('script')).toBeTruthy();
    expect(list.textContent).toContain('make the runner propagate the exit code');
  });
});

describe('ProposalFacets', () => {
  it('lists follow-ups (with and without a form) and warnings', () => {
    render(
      <ProposalFacets
        p={proposal({
          form: 'always-on',
          lens: 'procedure',
          suggested_followups: [
            { form: 'hook', text: 'block done while the last command failed' },
            { form: '', text: 'keep an eye on it' },
          ],
          warnings: ['always-on growth: plugins/core/agents/tech-lead.md gains 4 lines net (+5 −1)'],
        })}
      />,
    );
    const followups = screen.getByRole('list', { name: 'follow-ups' }).querySelectorAll('li');
    expect(followups).toHaveLength(2);
    expect(followups[0].textContent).toBe('hook — block done while the last command failed');
    expect(followups[1].textContent).toBe('keep an eye on it');
    const warnings = screen.getByRole('list', { name: 'warnings' });
    expect(warnings.textContent).toContain('gains 4 lines net');
    expect(screen.queryByTestId('proposal-keep-base-reason')).toBeNull();
  });

  it('renders nothing when there is nothing to add', () => {
    const { container } = render(<ProposalFacets p={proposal({ form: 'always-on', lens: 'procedure' })} />);
    expect(container.textContent).toBe('');
  });

  it('is translated in uk', async () => {
    await act(() => activate('uk'));
    render(
      <>
        <KeptBaseChip />
        <ProposalFormLensChips p={proposal({ form: 'script', lens: 'knowledge' })} />
        <ProposalFacets p={keptBase} />
      </>,
    );
    expect(screen.getByTestId('proposal-kept-base').textContent).toBe('залишено як є');
    expect(screen.getByTestId('proposal-form').textContent).toBe('скрипт');
    expect(screen.getByTestId('proposal-lens').textContent).toBe('знання');
    expect(screen.getByTestId('proposal-keep-base-reason').textContent).toContain('Чому файл залишається як є');
  });
});
