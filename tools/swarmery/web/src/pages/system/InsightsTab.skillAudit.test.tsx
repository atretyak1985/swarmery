// @vitest-environment jsdom
//
// Skill audit in the System hub (verify-not-trust phase 18). The claims:
//
//   1. skill_dead rides the Dead components list next to agent_dead, each row
//      badged with its kind.
//   2. The Skill audit section renders skill_desc_similar and
//      session_skill_load_high with the severity-toned chip, the pair's names or
//      the project, and the linter's message.
//   3. The latest weekly report is a row that opens into the report markdown;
//      with no report the section says how to get one, and a daemon older than
//      the audit (no skillAudit keys at all) still renders.
//   4. The Agents-tab hint links to #skill-audit with the report's date.
//   5. Labels read in English and Ukrainian.
//
// Runs with the rest of the web suite: `npm test`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SystemInsights, SystemSkillAuditReportBody } from '../../api/types';
import { setLocale } from '../../i18n/locale';
import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { InsightsTab } from './InsightsTab';
import { SkillsToExtendHint } from './SkillsToExtendHint';

const api = vi.hoisted(() => ({
  insights: vi.fn<() => Promise<SystemInsights>>(),
  report: vi.fn<() => Promise<SystemSkillAuditReportBody | null>>(),
}));

vi.mock('../../api/system', () => ({
  fetchSystemInsights: api.insights,
  fetchSkillAuditReport: api.report,
}));

const REPORT: SystemSkillAuditReportBody = {
  date: '2026-10-26',
  path: '/ws/swarmery/workspace/retro/skill-audit-2026-10-26.md',
  markdown: '# Skill audit 2026-10-26\n\n## Before a new agent, extend one of these\n\n- workspace-plans\n',
};

function payload(over: Partial<SystemInsights> = {}): SystemInsights {
  return {
    promotionCandidates: [],
    staleOverrides: [],
    undocumented: [],
    pluginDrift: [],
    dead: [
      { kind: 'agent', id: 6, name: 'legacy-bot', scope: 'global', projectSlug: null, message: 'agent msg', hint: 'h' },
      { kind: 'skill', id: 6, name: 'old-helper', scope: 'global', projectSlug: null, message: 'skill msg', hint: 'h' },
    ],
    skillAudit: [
      {
        rule: 'skill_desc_similar',
        severity: 'warn',
        message: 'skills "pdf-export" and "pdf-exporter": descriptions overlap 1.00',
        skills: ['pdf-export', 'pdf-exporter'],
        projectSlug: null,
      },
      {
        rule: 'session_skill_load_high',
        severity: 'warn',
        message: 'project "shop": p95 of 14 distinct skills per session',
        skills: [],
        projectSlug: 'shop',
      },
    ],
    skillAuditReport: { date: REPORT.date, path: REPORT.path },
    ...over,
  };
}

function renderTab(): void {
  render(
    <MemoryRouter>
      <InsightsTab refreshKey={0} projectNames={{ shop: 'Shop' }} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  api.insights.mockResolvedValue(payload());
  api.report.mockResolvedValue(REPORT);
});

afterEach(async () => {
  cleanup();
  vi.clearAllMocks();
  await setLocale('en');
});

describe('InsightsTab skill audit', () => {
  it('lists dead skills next to dead agents, badged by kind', async () => {
    renderTab();
    expect(await screen.findByText('legacy-bot')).toBeTruthy();
    expect(screen.getByText('old-helper')).toBeTruthy();
    const kinds = screen.getAllByText(/^(agent|skill)$/).map((el) => el.textContent);
    expect(kinds).toEqual(['agent', 'skill']);
    expect(screen.getByText('no use in 30 days, matched by name (advisory)')).toBeTruthy();
  });

  it('renders both rules with severity chips, names and messages', async () => {
    renderTab();
    const similar = await screen.findByText('similar skills');
    expect(similar.getAttribute('data-severity')).toBe('warn');
    expect(similar.className).toContain('text-amber');
    expect(screen.getByText(/^pdf-export\s+~\s+pdf-exporter$/)).toBeTruthy();
    expect(screen.getByText(/descriptions overlap 1\.00/)).toBeTruthy();

    const load = screen.getByText('skills per session');
    expect(load.getAttribute('data-severity')).toBe('warn');
    expect(screen.getByText('Shop')).toBeTruthy();
    expect(document.getElementById('skill-audit')).not.toBeNull();
  });

  it('opens the latest report into its markdown', async () => {
    renderTab();
    const row = await screen.findByText('Latest report · 2026-10-26');
    expect(api.report).not.toHaveBeenCalled();
    fireEvent.click(row);
    expect(await screen.findByText('Before a new agent, extend one of these')).toBeTruthy();
    expect(api.report).toHaveBeenCalledTimes(1);
  });

  it('says how to get a report when there is none, and survives an older daemon', async () => {
    const legacy = payload();
    delete legacy.skillAudit;
    delete legacy.skillAuditReport;
    api.insights.mockResolvedValue(legacy);
    renderTab();
    expect(
      await screen.findByText(
        'No weekly report yet — seed config/routines/skill-audit.json to write one every Monday.',
      ),
    ).toBeTruthy();
    expect(
      screen.getByText('no similar skill descriptions, and no project above the skills-per-session limit'),
    ).toBeTruthy();
  });

  it('reads in Ukrainian', async () => {
    await setLocale('uk');
    renderTab();
    expect(await screen.findByText('Аудит скілів')).toBeTruthy();
    expect(screen.getByText('схожі скіли')).toBeTruthy();
    expect(screen.getByText('скілів на сесію')).toBeTruthy();
    expect(screen.getByText('Останній звіт · 2026-10-26')).toBeTruthy();
  });
});

describe('SkillsToExtendHint', () => {
  it('links to the skill-audit section with the report date', async () => {
    render(
      <MemoryRouter>
        <SkillsToExtendHint insightsHref="/system/insights#skill-audit" />
      </MemoryRouter>,
    );
    expect(screen.getByText('Before proposing a new agent, extend an existing skill.')).toBeTruthy();
    const link = await screen.findByRole('link', { name: 'Skills to extend first — skill audit 2026-10-26' });
    expect(link.getAttribute('href')).toBe('/system/insights#skill-audit');
  });

  it('says how to get a report when there is none or the endpoint fails', async () => {
    api.report.mockRejectedValue(new Error('offline'));
    render(
      <MemoryRouter>
        <SkillsToExtendHint insightsHref="/system/insights#skill-audit" />
      </MemoryRouter>,
    );
    await waitFor(() =>
      expect(screen.getByText('No skill-audit report yet — seed config/routines/skill-audit.json.')).toBeTruthy(),
    );
    expect(screen.queryByRole('link')).toBeNull();
  });

  it('reads in Ukrainian', async () => {
    await setLocale('uk');
    render(
      <MemoryRouter>
        <SkillsToExtendHint insightsHref="/system/insights#skill-audit" />
      </MemoryRouter>,
    );
    expect(screen.getByText('Перш ніж пропонувати нового агента, розширте наявний скіл.')).toBeTruthy();
    expect(
      await screen.findByRole('link', { name: 'Скіли, які варто розширити першими — аудит скілів 2026-10-26' }),
    ).toBeTruthy();
  });
});
