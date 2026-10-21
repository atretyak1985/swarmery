// "In plain words": a short retelling of a plan doc for a person who did not
// write it — what a phase is for and what it closes, what a plan is about, what
// a finished phase or plan delivered. The daemon generates it once per doc
// version with a headless run and caches it (internal/plainsum); this
// component asks for the cached one first and, when it is missing, starts the
// generation itself, so opening the tab is all it takes.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import { fetchPlainSummary, generatePlainSummary, type PlainKind } from '../../api';
import { ErrorBox, Loading } from '../../components/ui';
import { Markdown } from '../../lib/markdown';

type State =
  | { kind: 'loading' }
  | { kind: 'generating' }
  | { kind: 'ready'; text: string }
  | { kind: 'empty' }
  | { kind: 'error'; message: string };

export interface PlainSummaryProps {
  taskId: number;
  kind: PlainKind;
  /** The phase doc's path inside plan/ (phase kinds only). */
  path?: string;
  /** Any value that changes when the source doc does — refetches. */
  version?: string | null;
}

export function PlainSummary({ taskId, kind, path, version }: PlainSummaryProps): JSX.Element | null {
  const { t, i18n } = useLingui();
  const lang = i18n.locale;
  const docPath = path ?? null;
  const [state, setState] = useState<State>({ kind: 'loading' });
  const [attempt, setAttempt] = useState(0);
  const retry = useCallback(() => setAttempt((n) => n + 1), []);

  // `version` and `attempt` are refetch triggers, not read inside.
  useEffect(() => {
    let alive = true;
    setState({ kind: 'loading' });
    const fail = (e: unknown): void => {
      if (alive) setState({ kind: 'error', message: e instanceof Error ? e.message : String(e) });
    };
    fetchPlainSummary(taskId, kind, docPath, lang)
      .then((got) => {
        if (!alive) return;
        if (got.status === 'ready') {
          setState({ kind: 'ready', text: got.text ?? '' });
          return;
        }
        if (got.status === 'empty') {
          setState({ kind: 'empty' });
          return;
        }
        setState({ kind: 'generating' });
        return generatePlainSummary(taskId, kind, docPath, lang).then((made) => {
          if (!alive) return;
          setState(made.status === 'ready' ? { kind: 'ready', text: made.text ?? '' } : { kind: 'empty' });
        });
      })
      .catch(fail);
    return () => {
      alive = false;
    };
  }, [taskId, kind, docPath, lang, version, attempt]);

  switch (state.kind) {
    case 'loading':
      return <Loading label={t`loading…`} />;
    case 'generating':
      return <Loading label={t`Claude is writing a plain-language explanation…`} />;
    case 'error':
      return <ErrorBox message={state.message} onRetry={retry} />;
    case 'empty':
      return (
        <div className="font-mono text-[11.5px] text-ink-faint">
          <Trans>nothing to explain yet — the doc has no text for this</Trans>
        </div>
      );
    case 'ready':
      return (
        <div className="text-[14px] leading-relaxed">
          <Markdown text={state.text} />
        </div>
      );
  }
}
