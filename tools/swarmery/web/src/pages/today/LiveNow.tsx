// "Live now" (handoff §3): the working sessions of the scope. One row each, no
// chips (§4): a session that is a plan phase's run (its uuid is the phase's
// runSessionUuid and the phase is running) shows the phase's progress ("Task n/m")
// and the model the run is on; any other shows how long it has been going and what
// it has cost. The link mod's live usage stays on the session's numbers strip. Each
// row opens the session detail in the current mount (lib/sessionHref.ts).

import { Trans, useLingui } from '@lingui/react/macro';
import type { Epic, EpicPhase, Session } from '../../api/types';
import { StateGlyph } from '../../components/place/StateGlyph';
import { fmtCost, projectLabel } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { compactSpan } from '../../lib/sessionsView';
import { PanelEmpty, PanelList, PanelRow, SectionHead, SectionLink } from './panel';

export const LIVE_ROWS = 5;

/** Running phase runs keyed by their session uuid. */
export function runningPhases(epics: readonly Epic[]): Map<string, EpicPhase> {
  const byUuid = new Map<string, EpicPhase>();
  for (const e of epics) {
    for (const p of e.phases) {
      if (p.runState === 'running' && p.runSessionUuid !== null) byUuid.set(p.runSessionUuid, p);
    }
  }
  return byUuid;
}

/** The model id without its `claude-` prefix; null when the session has none. */
function shortModel(model: string | null): string | null {
  return model === null || model === '' ? null : model.replace(/^claude-/, '');
}

function Row({ s, phase, now }: { s: Session; phase: EpicPhase | undefined; now: number }): JSX.Element {
  const { t } = useLingui();
  const sessionHref = useSessionHref();
  const project = projectLabel(s.projectName, s.projectSlug);
  const title = s.title ?? s.why ?? s.sessionUuid.slice(0, 8);
  let right: string;
  let sub: string;
  if (phase !== undefined) {
    const done = String(phase.checkboxesDone);
    const total = String(phase.checkboxesTotal);
    const model = shortModel(phase.runModel ?? s.modelLast ?? s.model) ?? t`model unknown`;
    right = t`Task ${done}/${total}`;
    sub = t`${project} · ${model} · plan run`;
  } else {
    right = compactSpan(now - Date.parse(s.startedAt));
    sub = `${project} · ${t`working`} · ${fmtCost(s.costUsd ?? null)}`;
  }
  return (
    <PanelRow
      testId="live-row"
      to={sessionHref(s.id)}
      glyph={<StateGlyph state="working" />}
      title={title}
      summary={sub}
      age={right}
    />
  );
}

export function LiveNow({
  sessions,
  epics,
  loading,
  now,
  sessionsHref,
}: {
  sessions: readonly Session[];
  epics: readonly Epic[];
  loading: boolean;
  now: number;
  sessionsHref: string;
}): JSX.Element {
  const { t } = useLingui();
  const phases = runningPhases(epics);
  const top = sessions.slice(0, LIVE_ROWS);
  const extra = sessions.length - LIVE_ROWS;
  return (
    <section data-testid="live-now" className="min-w-0">
      <SectionHead label={t`Live now`} tone="green">
        {extra > 0 && (
          <span className="font-mono text-[10.5px] leading-[normal] text-ink-faint">
            <Trans>+{extra} more</Trans>
          </span>
        )}
        <SectionLink to={sessionsHref}>
          <Trans>Sessions →</Trans>
        </SectionLink>
      </SectionHead>
      <div className="mt-2">
        {top.length === 0 ? (
          <PanelEmpty>{loading ? t`Loading…` : t`Nothing running.`}</PanelEmpty>
        ) : (
          <PanelList>
            {top.map((s) => (
              <Row key={s.id} s={s} phase={phases.get(s.sessionUuid)} now={now} />
            ))}
          </PanelList>
        )}
      </div>
    </section>
  );
}
