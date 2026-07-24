// Routines (fusion phase 7 — scheduled automation): list every cron/webhook/
// manual routine (name, scope, human-readable cron, enabled toggle, last/next
// run, last-status dot), create/edit them in a drawer with a typed steps builder
// (command | ai-prompt | create-task) and live cron validation, run one on
// demand, and expand a routine to see its pruned run history. Data comes from
// /api/routines{,/{id}/runs}; the global project scope (useScope) filters the
// list the same way Retro/Analytics do.

import { useCallback, useEffect, useMemo, useState } from 'react';
import type { Routine, RoutineInput, RoutineRun, RoutineStep, RoutineStepType } from '../api/types';
import {
  createRoutine,
  deleteRoutine,
  fetchRoutineRuns,
  fetchRoutines,
  patchRoutine,
  runRoutine,
} from '../api';
import { fmtAgo } from '../lib/format';
import { useScope } from '../lib/scope';
import { ConfirmDialog, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';
import { ProjectName } from '../components/ProjectName';

/* ---------------------------------------------------------------- cron help */

/** describeCron renders a terse human summary of the 5-field expressions the
 * UI is likely to produce; anything exotic falls back to the raw expression.
 * This is a display aid only — the daemon owns real parsing/validation. */
function describeCron(expr: string): string {
  const e = expr.trim();
  if (e === '') return 'manual / webhook only';
  const parts = e.split(/\s+/);
  if (parts.length !== 5) return e;
  const [min, hour, dom, mon, dow] = parts as [string, string, string, string, string];
  if (e === '* * * * *') return 'every minute';
  if (min !== '*' && hour !== '*' && dom === '*' && mon === '*' && dow === '*') {
    return `daily at ${pad(hour)}:${pad(min)}`;
  }
  if (min !== '*' && hour !== '*' && dow !== '*' && dom === '*' && mon === '*') {
    return `${weekday(dow)} at ${pad(hour)}:${pad(min)}`;
  }
  if (hour === '*' && min !== '*' && dom === '*' && mon === '*' && dow === '*') {
    return `hourly at :${pad(min)}`;
  }
  return e;
}

function pad(s: string): string {
  return s.length === 1 ? `0${s}` : s;
}
function weekday(dow: string): string {
  const names = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
  const n = Number.parseInt(dow, 10);
  return Number.isNaN(n) ? `dow ${dow}` : (names[n % 7] ?? `dow ${dow}`);
}

/** Loose client-side cron sanity check (5 fields of the allowed charset). The
 * server is authoritative; this only drives inline editor feedback. */
function cronLooksValid(expr: string): boolean {
  const e = expr.trim();
  if (e === '') return true; // blank = no schedule, allowed
  const parts = e.split(/\s+/);
  if (parts.length !== 5) return false;
  return parts.every((p) => /^[\d*,/-]+$/.test(p));
}

/* --------------------------------------------------------------- status dot */

function StatusDot({ status }: { status: RoutineRun['status'] | null }): JSX.Element {
  const tone =
    status === 'ok'
      ? 'bg-emerald-500'
      : status === 'failed'
        ? 'bg-red-500'
        : status === 'timeout'
          ? 'bg-amber-500'
          : status === 'running'
            ? 'bg-sky-500 animate-pulse'
            : 'bg-line-strong';
  const label = status ?? 'never run';
  return (
    <span className="inline-flex items-center gap-1.5" title={label}>
      <span className={`h-2 w-2 rounded-full ${tone}`} aria-hidden="true" />
      <span className="font-mono text-[10.5px] text-ink-faint">{label}</span>
    </span>
  );
}

/* ----------------------------------------------------------------- run list */

function RunHistory({ id }: { id: string }): JSX.Element {
  const [runs, setRuns] = useState<RoutineRun[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    fetchRoutineRuns(id)
      .then((r) => alive && setRuns(r))
      .catch((e: unknown) => alive && setErr(e instanceof Error ? e.message : String(e)));
    return () => {
      alive = false;
    };
  }, [id]);
  if (err) return <ErrorBox message={err} />;
  if (runs === null) return <Loading label="runs…" />;
  if (runs.length === 0) return <Empty>No runs yet.</Empty>;
  return (
    <ul className="flex flex-col gap-1.5">
      {runs.map((r) => (
        <li key={r.id} className="rounded-lg border border-line bg-field px-2.5 py-1.5">
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <StatusDot status={r.status} />
              <span className="font-mono text-[10.5px] text-ink-dim">{r.trigger}</span>
            </div>
            <span className="font-mono text-[10.5px] text-ink-faint">{fmtAgo(r.startedAt)}</span>
          </div>
          {r.detail !== null && (
            <pre className="mt-1 max-h-32 overflow-auto rounded border border-line bg-bg px-2 py-1 font-mono text-[10px] leading-snug text-ink-dim">
              {prettyDetail(r.detail)}
            </pre>
          )}
        </li>
      ))}
    </ul>
  );
}

function prettyDetail(detail: string): string {
  try {
    return JSON.stringify(JSON.parse(detail), null, 2);
  } catch {
    return detail;
  }
}

/* ------------------------------------------------------------------ row */

function RoutineRow({
  routine,
  onEdit,
  onChanged,
}: {
  routine: Routine;
  onEdit: (r: Routine) => void;
  onChanged: () => void;
}): JSX.Element {
  const { projects } = useScope();
  const [expanded, setExpanded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState<string | null>(null);

  const project = routine.projectId !== null ? projects.find((p) => p.id === routine.projectId) : null;

  const toggle = async (): Promise<void> => {
    setBusy(true);
    try {
      await patchRoutine(routine.id, { enabled: !routine.enabled, cronExpr: routine.cronExpr });
      onChanged();
    } catch (e) {
      setFlash(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const run = async (): Promise<void> => {
    setBusy(true);
    setFlash(null);
    try {
      const res = await runRoutine(routine.id);
      setFlash(res.status === 'started' ? 'run started' : 'busy — already running');
      if (expanded) onChanged();
    } catch (e) {
      setFlash(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <li className="rounded-xl border border-line bg-surface">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-3.5 py-3">
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
          aria-expanded={expanded}
        >
          <span className="font-mono text-[11px] text-ink-faint">{expanded ? '▾' : '▸'}</span>
          <span className="min-w-0">
            <span className="block truncate font-display text-[13px] font-semibold text-ink">
              {routine.name}
            </span>
            <span className="mt-0.5 flex items-center gap-2 font-mono text-[10.5px] text-ink-faint">
              {project ? (
                <ProjectName name={project.name} slug={project.slug} />
              ) : (
                <span className="text-ink-faint">global</span>
              )}
              <span>·</span>
              <span title={routine.cronExpr || 'no schedule'}>{describeCron(routine.cronExpr)}</span>
              {routine.hasWebhook && (
                <>
                  <span>·</span>
                  <span title="webhook trigger enabled">⚓ webhook</span>
                </>
              )}
            </span>
          </span>
        </button>

        <span className="hidden font-mono text-[10.5px] text-ink-faint sm:block">
          {routine.lastRunAt ? `last ${fmtAgo(routine.lastRunAt)}` : 'never run'}
        </span>
        <span className="hidden font-mono text-[10.5px] text-ink-faint sm:block">
          {routine.nextRunAt ? `next ${fmtAgo(routine.nextRunAt)}` : 'no next run'}
        </span>

        <label className="flex cursor-pointer items-center gap-1.5" title="enable / disable">
          <input
            type="checkbox"
            checked={routine.enabled}
            onChange={toggle}
            disabled={busy}
            className="h-3.5 w-3.5 accent-brand"
          />
          <span className="font-mono text-[10.5px] text-ink-dim">{routine.enabled ? 'on' : 'off'}</span>
        </label>

        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={run}
            disabled={busy}
            className="rounded-lg border border-line-strong px-2.5 py-1 font-mono text-[11px] text-ink-dim transition-colors hover:text-ink disabled:opacity-50"
          >
            Run now
          </button>
          <button
            type="button"
            onClick={() => onEdit(routine)}
            className="rounded-lg border border-line-strong px-2.5 py-1 font-mono text-[11px] text-ink-dim transition-colors hover:text-ink"
          >
            Edit
          </button>
        </div>
      </div>

      {flash !== null && (
        <div className="border-t border-line px-3.5 py-1.5 font-mono text-[10.5px] text-ink-dim">{flash}</div>
      )}

      {expanded && (
        <div className="border-t border-line px-3.5 py-3">
          <SectionTitle>Run history</SectionTitle>
          <div className="mt-2">
            <RunHistory id={routine.id} />
          </div>
        </div>
      )}
    </li>
  );
}

/* --------------------------------------------------------------- steps ui */

const STEP_TYPES: RoutineStepType[] = ['command', 'ai-prompt', 'create-task'];

function blankStep(type: RoutineStepType): RoutineStep {
  return { type, name: '' };
}

function StepEditor({
  step,
  onChange,
  onRemove,
}: {
  step: RoutineStep;
  onChange: (s: RoutineStep) => void;
  onRemove: () => void;
}): JSX.Element {
  const set = (patch: Partial<RoutineStep>): void => onChange({ ...step, ...patch });
  return (
    <div className="rounded-lg border border-line bg-field px-2.5 py-2.5">
      <div className="flex items-center gap-2">
        <select
          value={step.type}
          onChange={(e) => onChange({ ...blankStep(e.target.value as RoutineStepType), name: step.name })}
          className="rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-[11px] text-ink"
        >
          {STEP_TYPES.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        <input
          type="text"
          value={step.name}
          onChange={(e) => set({ name: e.target.value })}
          placeholder="step name"
          className="min-w-0 flex-1 rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-[11px] text-ink"
        />
        <button
          type="button"
          onClick={onRemove}
          aria-label="remove step"
          className="rounded-md border border-line px-2 py-1 font-mono text-[11px] text-ink-dim hover:text-red-400"
        >
          ×
        </button>
      </div>

      {step.type === 'command' && (
        <input
          type="text"
          value={step.command ?? ''}
          onChange={(e) => set({ command: e.target.value })}
          placeholder="shell command (e.g. curl -fsS -X POST …)"
          className="mt-2 w-full rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-[11px] text-ink"
        />
      )}
      {step.type === 'ai-prompt' && (
        <div className="mt-2 flex flex-col gap-2">
          <textarea
            value={step.prompt ?? ''}
            onChange={(e) => set({ prompt: e.target.value })}
            placeholder="prompt for the headless claude run"
            rows={2}
            className="w-full rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-[11px] text-ink"
          />
          <input
            type="text"
            value={step.model ?? ''}
            onChange={(e) => set({ model: e.target.value })}
            placeholder="model override (optional, e.g. sonnet)"
            className="w-full rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-[11px] text-ink"
          />
        </div>
      )}
      {step.type === 'create-task' && (
        <div className="mt-2 flex flex-col gap-2">
          <input
            type="text"
            value={step.taskTitle ?? ''}
            onChange={(e) => set({ taskTitle: e.target.value })}
            placeholder="task title"
            className="w-full rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-[11px] text-ink"
          />
          <textarea
            value={step.taskPrompt ?? ''}
            onChange={(e) => set({ taskPrompt: e.target.value })}
            placeholder="task prompt (board card body)"
            rows={2}
            className="w-full rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-[11px] text-ink"
          />
          <input
            type="text"
            value={step.boardColumn ?? ''}
            onChange={(e) => set({ boardColumn: e.target.value })}
            placeholder="board column (default: triage)"
            className="w-full rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-[11px] text-ink"
          />
        </div>
      )}

      {(step.type === 'command' || step.type === 'ai-prompt') && (
        <label className="mt-2 flex items-center gap-1.5 font-mono text-[10.5px] text-ink-dim">
          <input
            type="checkbox"
            checked={step.continueOnFailure ?? false}
            onChange={(e) => set({ continueOnFailure: e.target.checked })}
            className="h-3 w-3 accent-brand"
          />
          continue on failure
        </label>
      )}
    </div>
  );
}

/* ------------------------------------------------------------------ editor */

function RoutineEditor({
  routine,
  onClose,
  onSaved,
}: {
  routine: Routine | 'new';
  onClose: () => void;
  onSaved: () => void;
}): JSX.Element {
  const { projects } = useScope();
  const isNew = routine === 'new';
  const seed: Routine | null = isNew ? null : routine;

  const [name, setName] = useState(seed?.name ?? '');
  const [projectId, setProjectId] = useState<number | null>(seed?.projectId ?? null);
  const [cronExpr, setCronExpr] = useState(seed?.cronExpr ?? '');
  const [catchUp, setCatchUp] = useState<'skip' | 'run_one'>(seed?.catchUp ?? 'skip');
  const [timeoutSec, setTimeoutSec] = useState(seed?.timeoutSec ?? 900);
  const [webhook, setWebhook] = useState(seed?.hasWebhook ?? false);
  const [steps, setSteps] = useState<RoutineStep[]>(seed?.steps ?? [blankStep('command')]);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [token, setToken] = useState<string | null>(null);

  const cronOk = cronLooksValid(cronExpr);
  const canSave = name.trim() !== '' && steps.length > 0 && cronOk && !saving;

  const save = async (): Promise<void> => {
    setSaving(true);
    setErr(null);
    const input: RoutineInput = {
      name: name.trim(),
      projectId,
      cronExpr,
      catchUp,
      timeoutSec,
      steps,
      webhook,
    };
    try {
      const saved = isNew ? await createRoutine(input) : await patchRoutine(seed!.id, input);
      if (saved.webhookToken) {
        setToken(saved.webhookToken);
        return; // keep the drawer open to surface the one-time token
      }
      onSaved();
      onClose();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex justify-end bg-bg/60"
      role="dialog"
      aria-modal="true"
      aria-label={isNew ? 'new routine' : 'edit routine'}
      onClick={onClose}
    >
      <div
        className="flex h-full w-full max-w-lg flex-col border-l border-line bg-surface"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-line px-4 py-3">
          <span className="font-display text-[14px] font-bold text-ink">
            {isNew ? 'New routine' : 'Edit routine'}
          </span>
          <button
            type="button"
            onClick={onClose}
            aria-label="close"
            className="rounded-lg border border-line px-2.5 py-1 font-mono text-[12px] text-ink-dim hover:text-ink"
          >
            ×
          </button>
        </div>

        <div className="flex-1 space-y-3.5 overflow-y-auto px-4 py-4">
          {token !== null && (
            <div className="rounded-lg border border-emerald-500/40 bg-emerald-500/10 px-3 py-2.5">
              <div className="font-mono text-[11px] font-semibold text-emerald-300">
                Webhook token (shown once — copy it now)
              </div>
              <code className="mt-1 block break-all font-mono text-[11px] text-ink">{token}</code>
              <button
                type="button"
                onClick={() => {
                  onSaved();
                  onClose();
                }}
                className="mt-2 rounded-lg border border-line-strong px-3 py-1 font-mono text-[11px] text-ink-dim hover:text-ink"
              >
                Done
              </button>
            </div>
          )}

          <Field label="Name">
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="nightly advisor re-run"
              className="w-full rounded-lg border border-line-strong bg-field px-2.5 py-1.5 font-mono text-[12px] text-ink"
            />
          </Field>

          <Field label="Scope">
            <select
              value={projectId ?? ''}
              onChange={(e) => setProjectId(e.target.value === '' ? null : Number.parseInt(e.target.value, 10))}
              className="w-full rounded-lg border border-line-strong bg-field px-2.5 py-1.5 font-mono text-[12px] text-ink"
            >
              <option value="">Global (daemon cwd)</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name ?? p.slug}
                </option>
              ))}
            </select>
          </Field>

          <Field label="Cron (5-field, blank = manual/webhook only)">
            <input
              type="text"
              value={cronExpr}
              onChange={(e) => setCronExpr(e.target.value)}
              placeholder="0 3 * * *"
              className={`w-full rounded-lg border bg-field px-2.5 py-1.5 font-mono text-[12px] text-ink ${
                cronOk ? 'border-line-strong' : 'border-red-500'
              }`}
            />
            <div className="mt-1 font-mono text-[10.5px] text-ink-faint">
              {cronOk ? describeCron(cronExpr) : 'invalid cron — need 5 fields'}
            </div>
          </Field>

          <div className="flex gap-3">
            <Field label="Catch-up">
              <select
                value={catchUp}
                onChange={(e) => setCatchUp(e.target.value as 'skip' | 'run_one')}
                className="w-full rounded-lg border border-line-strong bg-field px-2.5 py-1.5 font-mono text-[12px] text-ink"
              >
                <option value="skip">skip (drop missed)</option>
                <option value="run_one">run_one (one catch-up)</option>
              </select>
            </Field>
            <Field label="Timeout (s)">
              <input
                type="number"
                min={1}
                value={timeoutSec}
                onChange={(e) => setTimeoutSec(Math.max(1, Number.parseInt(e.target.value, 10) || 1))}
                className="w-full rounded-lg border border-line-strong bg-field px-2.5 py-1.5 font-mono text-[12px] text-ink"
              />
            </Field>
          </div>

          <label className="flex items-center gap-2 font-mono text-[11px] text-ink-dim">
            <input
              type="checkbox"
              checked={webhook}
              onChange={(e) => setWebhook(e.target.checked)}
              className="h-3.5 w-3.5 accent-brand"
            />
            enable webhook trigger (POST /api/hooks/routine/{'{id}'}/{'{token}'})
          </label>

          <div>
            <div className="mb-1.5 flex items-center justify-between">
              <SectionTitle>Steps</SectionTitle>
              <button
                type="button"
                onClick={() => setSteps((s) => [...s, blankStep('command')])}
                className="rounded-lg border border-line-strong px-2.5 py-1 font-mono text-[11px] text-ink-dim hover:text-ink"
              >
                + add step
              </button>
            </div>
            <div className="flex flex-col gap-2">
              {steps.map((s, i) => (
                <StepEditor
                  key={i}
                  step={s}
                  onChange={(next) => setSteps((arr) => arr.map((x, j) => (j === i ? next : x)))}
                  onRemove={() => setSteps((arr) => arr.filter((_, j) => j !== i))}
                />
              ))}
              {steps.length === 0 && <Empty>Add at least one step.</Empty>}
            </div>
          </div>

          {err !== null && <ErrorBox message={err} />}
        </div>

        <div className="flex justify-end gap-2 border-t border-line px-4 py-3">
          <button
            type="button"
            onClick={onClose}
            className="rounded-lg border border-line px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 hover:bg-surface2"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={save}
            disabled={!canSave}
            className="rounded-lg border border-brand/40 bg-brand/10 px-3.5 py-1.5 font-mono text-[11.5px] text-brand transition-colors hover:bg-brand/20 disabled:opacity-40"
          >
            {saving ? 'Saving…' : isNew ? 'Create' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }): JSX.Element {
  return (
    <label className="block flex-1">
      <span className="mb-1 block font-mono text-[10.5px] tracking-wide text-ink-faint uppercase">{label}</span>
      {children}
    </label>
  );
}

/* -------------------------------------------------------------------- page */

export function Routines(): JSX.Element {
  const { scope, projects } = useScope();
  const scopeProjectId = useMemo(() => {
    if (scope === null) return undefined;
    return projects.find((p) => p.slug === scope)?.id;
  }, [scope, projects]);

  const [routines, setRoutines] = useState<Routine[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [editing, setEditing] = useState<Routine | 'new' | null>(null);
  const [confirmDel, setConfirmDel] = useState<Routine | null>(null);
  const [delBusy, setDelBusy] = useState(false);

  const load = useCallback(() => {
    setErr(null);
    fetchRoutines(scopeProjectId)
      .then(setRoutines)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)));
  }, [scopeProjectId]);

  useEffect(load, [load]);

  const doDelete = async (): Promise<void> => {
    if (!confirmDel) return;
    setDelBusy(true);
    try {
      await deleteRoutine(confirmDel.id);
      setConfirmDel(null);
      load();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setDelBusy(false);
    }
  };

  return (
    <div className="mx-auto max-w-4xl px-4 py-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="font-display text-[18px] font-bold text-ink">Routines</h1>
          <p className="mt-0.5 font-mono text-[11px] text-ink-faint">
            Scheduled automation — cron / webhook / manual, with typed steps and run history.
          </p>
        </div>
        <button
          type="button"
          onClick={() => setEditing('new')}
          className="rounded-lg border border-brand/40 bg-brand/10 px-3 py-1.5 font-mono text-[11.5px] text-brand transition-colors hover:bg-brand/20"
        >
          + New routine
        </button>
      </div>

      <div className="mt-5">
        {err !== null && <ErrorBox message={err} />}
        {routines === null && err === null && <Loading label="routines…" />}
        {routines !== null && routines.length === 0 && (
          <Empty>No routines yet. Create one to schedule recurring work.</Empty>
        )}
        {routines !== null && routines.length > 0 && (
          <ul className="flex flex-col gap-2.5">
            {routines.map((r) => (
              <RoutineRow key={r.id} routine={r} onEdit={setEditing} onChanged={load} />
            ))}
          </ul>
        )}
      </div>

      {editing !== null && (
        <RoutineEditor
          routine={editing}
          onClose={() => setEditing(null)}
          onSaved={load}
        />
      )}

      {editing !== null && editing !== 'new' && (
        <div className="mt-4 flex justify-end">
          <button
            type="button"
            onClick={() => {
              setConfirmDel(editing);
              setEditing(null);
            }}
            className="font-mono text-[11px] text-red-400 hover:text-red-300"
          >
            Delete this routine
          </button>
        </div>
      )}

      <ConfirmDialog
        open={confirmDel !== null}
        title="Delete routine?"
        confirmLabel="Delete"
        danger
        busy={delBusy}
        onConfirm={doDelete}
        onCancel={() => setConfirmDel(null)}
      >
        {confirmDel && (
          <>
            Permanently delete <span className="font-semibold text-ink">{confirmDel.name}</span> and its run
            history? This cannot be undone.
          </>
        )}
      </ConfirmDialog>
    </div>
  );
}
