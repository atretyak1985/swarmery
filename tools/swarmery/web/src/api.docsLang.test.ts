// fetchDocs/fetchDoc ask the daemon for the active UI language
// (dashboard-uk-locale phase 5): `?lang=uk` under a Ukrainian interface,
// `?lang=en` otherwise — the daemon falls back to English per doc itself.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchDoc, fetchDocs } from './api';
import { activate } from './i18n';

const fetchMock = vi.fn(
  async (_url: string) => new Response('[]', { status: 200, headers: { 'Content-Type': 'application/json' } }),
);

afterEach(async () => {
  vi.unstubAllGlobals();
  fetchMock.mockClear();
  await activate('en');
});

describe('docs language parameter', () => {
  it('asks for English under the English interface', async () => {
    vi.stubGlobal('fetch', fetchMock);
    await fetchDocs();
    await fetchDoc('getting started');
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual([
      '/api/docs?lang=en',
      '/api/docs/getting%20started?lang=en',
    ]);
  });

  it('asks for Ukrainian under the Ukrainian interface', async () => {
    await activate('uk');
    vi.stubGlobal('fetch', fetchMock);
    await fetchDocs();
    await fetchDoc('onboarding');
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual(['/api/docs?lang=uk', '/api/docs/onboarding?lang=uk']);
  });
});
