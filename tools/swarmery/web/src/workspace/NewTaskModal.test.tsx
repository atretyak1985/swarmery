// @vitest-environment jsdom
//
// Intake tests for the new-task modal (board redesign v2 phase 3): the form is
// two fields and one button until someone asks for more.
//
// The fence that matters is the FIELD COUNT, asserted on the DOM rather than by
// eye. This form carried ten controls for a card whose only required value is a
// title, and every one of them had a working default; "collapsed" that still
// renders the controls behind a class would keep them in the tab order and put
// the count straight back. Counting input/textarea nodes is the one assertion a
// future re-expansion cannot pass by accident.
//
// The second fence is the POST body. Trimming the form must not change the wire
// contract — the same keys, and the same absent ones — or a card created from
// the new form and one created from the old would differ in the database.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/workspace/NewTaskModal.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom
// (none of them are committed dependencies). web/tsconfig.json EXCLUDES
// *.test.tsx, so `npm run build` does NOT type-check this file.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AgentRosterRow } from '../api/types';
import { NewTaskModal } from './NewTaskModal';

function rosterRow(name: string): AgentRosterRow {
  return {
    id: 1,
    name,
    scope: 'global',
    projectSlug: null,
    origin: 'plugin',
    pluginName: 'core',
    model: null,
    path: `/agents/${name}.md`,
    description: null,
    improvable: true,
    runs30d: 0,
    successRate: null,
    failedShare: 0,
    cost30d: 0,
    lastActiveAt: null,
  };
}

/** Bodies of every POST /api/board/tasks the component made. */
let posted: Record<string, unknown>[] = [];

beforeEach(() => {
  posted = [];
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.startsWith('/api/agents/hub')) {
        return Promise.resolve(
          new Response(JSON.stringify({ agents: [rosterRow('core:implementation-agent')] }), {
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      if (url.startsWith('/api/playbooks')) {
        return Promise.resolve(
          new Response(JSON.stringify([]), { headers: { 'Content-Type': 'application/json' } }),
        );
      }
      if (url === '/api/board/tasks') {
        posted.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
        return Promise.resolve(
          new Response(JSON.stringify({ id: 7, externalId: 'T-new001' }), {
            status: 201,
            headers: { 'Content-Type': 'application/json' },
          }),
        );
      }
      throw new Error(`unexpected fetch: ${url}`);
    }),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

/** Renders the modal and waits for the roster fetch to settle. */
async function open(initialText = ''): Promise<HTMLElement> {
  const { container } = render(
    <NewTaskModal
      projectId={1}
      projectSlug="p"
      initialText={initialText}
      onCreated={() => {}}
      onClose={() => {}}
    />,
  );
  await waitFor(() => {
    expect(screen.getByLabelText('title')).toBeDefined();
  });
  return container;
}

describe('NewTaskModal — collapsed intake', () => {
  it('shows exactly two fields and one action until advanced is opened', async () => {
    const container = await open();

    const fields = container.querySelectorAll('input, textarea');
    expect(fields.length).toBe(2);
    expect(screen.getByLabelText('title')).toBeDefined();
    expect(screen.getByLabelText('what needs doing')).toBeDefined();

    // The other eight controls are not in the document at all — not merely
    // styled away, which would leave them tabbable.
    expect(container.querySelectorAll('select').length).toBe(0);
    expect(screen.queryByLabelText('priority')).toBeNull();
    expect(screen.queryByLabelText('column')).toBeNull();

    // One action, and it names where the card lands.
    expect(screen.getByRole('button', { name: 'create in Inbox' })).toBeDefined();
    expect(screen.queryByRole('button', { name: 'cancel' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'create task' })).toBeNull();
  });

  it('puts all eight former fields under advanced', async () => {
    const container = await open();
    fireEvent.click(screen.getByRole('button', { name: /advanced/ }));

    for (const label of [
      'agent',
      'priority',
      'model',
      'playbook',
      'file scope',
      'dependencies',
      'labels',
      'column',
    ]) {
      expect(screen.getByLabelText(label), `advanced is missing "${label}"`).toBeDefined();
    }
    // 2 visible + the three chip editors' add-inputs.
    expect(container.querySelectorAll('input, textarea').length).toBe(5);
  });

  it('names the advanced values that are set while the section is closed', async () => {
    await open();
    const toggle = screen.getByRole('button', { name: /advanced/ });
    fireEvent.click(toggle);
    fireEvent.change(screen.getByLabelText('priority'), { target: { value: 'urgent' } });
    fireEvent.click(screen.getByRole('button', { name: /advanced/ }));

    // Collapsing must not hide the fact that priority is no longer 'normal'.
    expect(screen.getByRole('button', { name: /advanced · priority/ })).toBeDefined();
  });

  it('tracks the landing column in the action label', async () => {
    await open();
    fireEvent.click(screen.getByRole('button', { name: /advanced/ }));
    fireEvent.change(screen.getByLabelText('column'), { target: { value: 'todo' } });

    expect(screen.queryByRole('button', { name: 'create in Inbox' })).toBeNull();
    expect(screen.getByRole('button', { name: 'create in Working' })).toBeDefined();
  });

  it('opens advanced for a ?compose= deep link so the resolved agent is visible', async () => {
    await open('@core:implementation-agent: fix the retry helper');

    await waitFor(() => {
      expect((screen.getByLabelText('agent') as HTMLSelectElement).value).toBe(
        'core:implementation-agent',
      );
    });
    expect((screen.getByLabelText('title') as HTMLInputElement).value).toBe('fix the retry helper');
  });
});

describe('NewTaskModal — POST body', () => {
  it('sends the title as the prompt when the prompt is empty, and omits unset values', async () => {
    await open();
    fireEvent.change(screen.getByLabelText('title'), { target: { value: 'add a --json flag' } });
    fireEvent.click(screen.getByRole('button', { name: 'create in Inbox' }));

    await waitFor(() => {
      expect(posted.length).toBe(1);
    });
    const body = posted[0] as Record<string, unknown>;
    // The intake contract the board has always had.
    expect(body['prompt']).toBe('add a --json flag');
    expect(body['title']).toBe('add a --json flag');
    // Unchanged from the ten-field form: these keys ride every create…
    expect(body['priority']).toBe('normal');
    expect(body['boardColumn']).toBe('triage');
    expect(body['fileScope']).toEqual([]);
    expect(body['dependencies']).toEqual([]);
    expect(body['labels']).toEqual([]);
    // …and these three are omitted, not sent empty, when nothing set them.
    expect('model' in body).toBe(false);
    expect('playbook' in body).toBe(false);
    expect('agent' in body).toBe(false);
  });

  it('sends what needs doing verbatim when it is filled in', async () => {
    await open();
    fireEvent.change(screen.getByLabelText('title'), { target: { value: 'retry helper' } });
    fireEvent.change(screen.getByLabelText('what needs doing'), {
      target: { value: 'Extract it into internal/retry and cover the backoff.' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'create in Inbox' }));

    await waitFor(() => {
      expect(posted.length).toBe(1);
    });
    expect(posted[0]?.['prompt']).toBe('Extract it into internal/retry and cover the backoff.');
    expect(posted[0]?.['title']).toBe('retry helper');
  });

  it('carries an advanced override into the POST', async () => {
    await open();
    fireEvent.change(screen.getByLabelText('title'), { target: { value: 'urgent thing' } });
    fireEvent.click(screen.getByRole('button', { name: /advanced/ }));
    fireEvent.change(screen.getByLabelText('priority'), { target: { value: 'urgent' } });
    fireEvent.change(screen.getByLabelText('column'), { target: { value: 'todo' } });
    fireEvent.click(screen.getByRole('button', { name: 'create in Working' }));

    await waitFor(() => {
      expect(posted.length).toBe(1);
    });
    expect(posted[0]?.['priority']).toBe('urgent');
    expect(posted[0]?.['boardColumn']).toBe('todo');
  });
});
