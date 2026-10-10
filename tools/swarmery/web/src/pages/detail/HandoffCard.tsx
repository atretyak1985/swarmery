// Detail-rail "Handoff" section: shown when the daemon has generated a
// continuation brief for this fat session (migration 0039). Collapsed by
// default; expanding fetches GET /api/sessions/{id}/handoff and renders the
// markdown in the rail's prose style. A copy button yields the exact resume
// command for a fresh session.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useState } from 'react';
import { fetchSessionHandoff } from '../../api';
import type { SessionHandoffResponse } from '../../api/types';
import { Markdown } from '../../lib/markdown';

/** The resume command a fresh session starts from — reads the brief and picks up. */
function resumeCommand(path: string): string {
  // A shell command pasted into a terminal — code, not copy.
  // i18n-ignore
  return `claude "Read ${path} and continue the task from where it left off"`;
}

export function HandoffCard({
  sessionId,
  handoffPath,
}: {
  sessionId: number;
  handoffPath: string;
}): JSX.Element {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<SessionHandoffResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const toggle = useCallback(() => {
    const next = !open;
    setOpen(next);
    if (next && data === null && !loading) {
      setLoading(true);
      setError(null);
      fetchSessionHandoff(sessionId)
        .then((d) => setData(d))
        .catch((e: unknown) => setError(e instanceof Error ? e.message : t`failed to load handoff`))
        .finally(() => setLoading(false));
    }
  }, [open, data, loading, sessionId, t]);

  const copyResume = useCallback(() => {
    const cmd = resumeCommand(data?.path ?? handoffPath);
    void navigator.clipboard.writeText(cmd).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }, [data, handoffPath]);

  return (
    <div className="rounded-xl border border-violet-500/30 bg-surface px-4 py-3.5">
      <button
        type="button"
        onClick={toggle}
        className="flex w-full items-baseline justify-between text-left"
      >
        <span className="font-mono text-[10.5px] tracking-[0.08em] text-violet-400 uppercase">
          <Trans>handoff</Trans>
        </span>
        <span className="font-mono text-[11px] text-ink-dim">{open ? t`hide` : t`show`}</span>
      </button>

      {open && (
        <div className="mt-3">
          <button
            type="button"
            onClick={copyResume}
            data-tip={t`Copy the command that starts a fresh session from this handoff`}
            className="mb-3 w-full rounded-lg border border-violet-500/40 bg-violet-500/10 px-3 py-2 text-left font-mono text-[11px] text-violet-300 transition-colors hover:bg-violet-500/20"
          >
            {copied ? t`copied ✓` : t`copy resume command`}
          </button>

          {loading && <p className="font-mono text-[11px] text-ink-dim">
              <Trans>loading…</Trans>
            </p>}
          {error !== null && <p className="font-mono text-[11px] text-red">{error}</p>}
          {data !== null && (
            <div className="prose-rail max-w-none text-[12px] text-ink-2">
              <Markdown text={data.markdown} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}
