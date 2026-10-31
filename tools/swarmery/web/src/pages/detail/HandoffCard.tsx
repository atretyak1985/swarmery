// The session rail's handoff card: shown when the daemon has generated a
// continuation brief for this fat session (migration 0039). It says the brief is
// ready and offers the resume command; the header expands the brief itself, which
// fetches GET /api/sessions/{id}/handoff and renders in the rail's prose style. The
// design's button reads "continue in a fresh session"; this one only copies, so it
// says that (D-6).

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useLocation } from 'react-router-dom';
import { fetchSessionHandoff } from '../../api';
import type { Session, SessionHandoffResponse } from '../../api/types';
import { fmtTokens } from '../../lib/format';
import { Markdown } from '../../lib/markdown';
import { usePanels } from '../../lib/panels';
import { compactSpan } from '../../lib/sessionsView';

/** The resume command a fresh session starts from — reads the brief and picks up. */
function resumeCommand(path: string): string {
  // A shell command pasted into a terminal — code, not copy.
  // i18n-ignore
  return `claude "Read ${path} and continue the task from where it left off"`;
}

export function HandoffCard({
  sessionId,
  handoff,
  hot,
  nowMs,
}: {
  sessionId: number;
  handoff: NonNullable<Session['handoff']>;
  /** The context this brief escapes from is fat: the card turns red. */
  hot: boolean;
  nowMs: number;
}): JSX.Element {
  const { t } = useLingui();
  const handoffPath = handoff.path;
  const { hash } = useLocation();
  const { rail: railCollapsed } = usePanels();
  const ref = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<SessionHandoffResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const load = useCallback(() => {
    if (data !== null || loading) return;
    setLoading(true);
    setError(null);
    fetchSessionHandoff(sessionId)
      .then((d) => setData(d))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : t`failed to load handoff`))
      .finally(() => setLoading(false));
  }, [data, loading, sessionId, t]);

  const toggle = useCallback(() => {
    const next = !open;
    setOpen(next);
    if (next) load();
  }, [open, load]);

  // «continue from handoff» (CacheColdBadge) lands here as #handoff: open the brief.
  const arrived = hash === '#handoff';
  useEffect(() => {
    if (!arrived) return;
    setOpen(true);
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per arrival, not per load state
  }, [arrived]);

  // Bring it into view once the rail shows it, a hidden node cannot scroll, and again
  // when the brief lands and gives the card its height.
  const briefLoaded = data !== null;
  useEffect(() => {
    if (arrived && !railCollapsed) ref.current?.scrollIntoView?.({ block: 'nearest' });
  }, [arrived, railCollapsed, briefLoaded]);

  const copyResume = useCallback(() => {
    const cmd = resumeCommand(data?.path ?? handoffPath);
    void navigator.clipboard.writeText(cmd).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }, [data, handoffPath]);

  const tone = hot ? 'text-red' : 'text-brand';
  const age = compactSpan(nowMs - new Date(handoff.createdAt).getTime());
  const tokens = fmtTokens(handoff.contextTokens);

  return (
    <div
      id="handoff"
      ref={ref}
      className={`rounded-[9px] border px-3 py-2.5 ${hot ? 'border-red bg-red-bg' : 'border-brand bg-brand-bg'}`}
    >
      <button
        type="button"
        onClick={toggle}
        aria-expanded={open}
        className={`flex w-full items-center gap-1.5 text-left font-mono text-[10px] leading-[normal] tracking-[0.12em] uppercase ${tone}`}
      >
        <Trans>handoff</Trans>
        <span className="ml-auto tracking-normal text-ink-faint normal-case">
          {t`ready · ${age} ago`} <span aria-hidden="true">{open ? '▾' : '▸'}</span>
        </span>
      </button>
      <p className="m-0 mt-1.5 text-[12.5px] leading-[1.5] text-ink-2">
        {hot
          ? t`Context is past ${tokens}; every turn re-reads it. A brief for a fresh session is ready.`
          : t`The daemon wrote a brief for a fresh session.`}
      </p>
      <button
        type="button"
        onClick={copyResume}
        data-tip={t`Copy the command that starts a fresh session from this handoff`}
        className={`mt-2 rounded-[6px] border border-current bg-transparent px-2.5 py-1 font-mono text-[10.5px] leading-[normal] ${tone}`}
      >
        {copied ? t`copied ✓` : t`copy resume command`}
      </button>

      {open && (
        // The rail is sticky, so a brief taller than the window would run off its foot.
        <div
          role="region"
          aria-label={t`handoff brief`}
          tabIndex={0}
          className="mt-3 max-h-[50vh] overflow-y-auto"
        >
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
