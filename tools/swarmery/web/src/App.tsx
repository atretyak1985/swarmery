import { useEffect, useState } from 'react';
import { fetchSession, fetchSessions } from './api';
import type { Session, SessionDetail } from './api/types';

// Single skeleton page: sessions list → raw detail view. No design polish yet.
export function App(): JSX.Element {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [detail, setDetail] = useState<SessionDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetchSessions()
      .then(setSessions)
      .catch((e: unknown) => setError(String(e)));
  }, []);

  const openSession = (id: number): void => {
    setDetail(null);
    fetchSession(id)
      .then(setDetail)
      .catch((e: unknown) => setError(String(e)));
  };

  return (
    <main className="mx-auto max-w-5xl p-4 font-mono text-sm">
      <h1 className="mb-4 text-lg font-bold">swarmery — sessions</h1>

      {error !== null && <p className="mb-4 text-red-600">{error}</p>}

      {detail === null ? (
        <table className="w-full border-collapse">
          <thead>
            <tr className="border-b text-left">
              <th className="p-1">project</th>
              <th className="p-1">title</th>
              <th className="p-1">status</th>
              <th className="p-1">model</th>
              <th className="p-1">started</th>
            </tr>
          </thead>
          <tbody>
            {sessions.map((s) => (
              <tr
                key={s.id}
                className="cursor-pointer border-b hover:bg-gray-100"
                onClick={() => openSession(s.id)}
              >
                <td className="p-1">{s.projectSlug}</td>
                <td className="p-1">{s.title ?? s.sessionUuid}</td>
                <td className="p-1">{s.status}</td>
                <td className="p-1">{s.model ?? '—'}</td>
                <td className="p-1">{s.startedAt}</td>
              </tr>
            ))}
            {sessions.length === 0 && (
              <tr>
                <td className="p-2 text-gray-500" colSpan={5}>
                  no sessions ingested yet — run: swarmery ingest &lt;file.jsonl&gt;
                </td>
              </tr>
            )}
          </tbody>
        </table>
      ) : (
        <section>
          <button
            type="button"
            className="mb-3 rounded border px-2 py-1 hover:bg-gray-100"
            onClick={() => setDetail(null)}
          >
            ← back to sessions
          </button>
          <h2 className="mb-2 font-bold">
            {detail.title ?? detail.sessionUuid} ({detail.status}) — {detail.turns.length} turns,{' '}
            {detail.events.length} events, {detail.fileChanges.length} file changes
          </h2>
          <pre className="overflow-x-auto whitespace-pre-wrap rounded bg-gray-100 p-3">
            {JSON.stringify(detail, null, 2)}
          </pre>
        </section>
      )}
    </main>
  );
}
