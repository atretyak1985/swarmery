// "Live now" (Canvas v3 phase 4, artboard 1a): the running sessions of the
// scope, newest first. A session that is a plan phase's run (its uuid is the
// phase's runSessionUuid and the phase is running) shows the phase's progress
// ("Task n/m") and the model the run is on; any other shows how long it has
// been going and what it has cost. Each row opens the session detail in the
// current mount (lib/sessionHref.ts).

import { Link } from 'react-router-dom';
import type { Epic, EpicPhase, Session } from '../../api/types';
import { fmtCost, projectLabel } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';
import { ageLabel } from '../inbox/inboxModel';
import { SectionHead } from './WaitingOnYou';

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

function shortModel(model: string | null): string {
  return model === null || model === '' ? 'model unknown' : model.replace(/^claude-/, '');
}

function Row({ s, phase, now }: { s: Session; phase: EpicPhase | undefined; now: number }): JSX.Element {
  const sessionHref = useSessionHref();
  const project = projectLabel(s.projectName, s.projectSlug);
  const title = s.title ?? s.why ?? s.sessionUuid.slice(0, 8);
  const right =
    phase !== undefined
      ? `Task ${String(phase.checkboxesDone)}/${String(phase.checkboxesTotal)}`
      : ageLabel(s.startedAt, now);
  const sub =
    phase !== undefined
      ? `${project} · ${shortModel(phase.runModel ?? s.modelLast ?? s.model)} · plan run`
      : `${project} · ${s.status === 'waiting_approval' ? 'waiting on you' : 'working'} · ${fmtCost(s.costUsd ?? null)}`;
  return (
    <li>
      <Link
        to={sessionHref(s.id)}
        data-testid="live-row"
        className="block rounded-[10px] border border-line bg-surface px-3 py-2.5 hover:border-line-strong"
      >
        <div className="flex items-center gap-2">
          <span aria-hidden className="size-[7px] shrink-0 rounded-full bg-green" />
          <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{title}</span>
          <span className="shrink-0 font-mono text-[10px] text-ink-faint">{right}</span>
        </div>
        <div className="mt-1 truncate font-mono text-[10.5px] text-ink-faint">{sub}</div>
      </Link>
    </li>
  );
}

export function LiveNow({
  sessions,
  epics,
  loading,
  now,
}: {
  sessions: readonly Session[];
  epics: readonly Epic[];
  loading: boolean;
  now: number;
}): JSX.Element {
  const phases = runningPhases(epics);
  const top = sessions.slice(0, LIVE_ROWS);
  return (
    <section data-testid="live-now" className="min-w-0">
      <SectionHead label="Live now">
        {sessions.length > LIVE_ROWS && (
          <span className="font-mono text-[10.5px] text-ink-faint">+{sessions.length - LIVE_ROWS} more</span>
        )}
      </SectionHead>
      {top.length === 0 ? (
        <p className="mt-2.5 rounded-[10px] border border-line bg-surface px-3 py-2.5 text-[12.5px] text-ink-dim">
          {loading ? 'Loading…' : 'Nothing running. A session shows up here the moment it starts.'}
        </p>
      ) : (
        <ul className="m-0 mt-2.5 flex list-none flex-col gap-1.5 p-0">
          {top.map((s) => (
            <Row key={s.id} s={s} phase={phases.get(s.sessionUuid)} now={now} />
          ))}
        </ul>
      )}
    </section>
  );
}
