// "+ new agent" form (System → Agents, step-12): client-side kebab-case name
// validation, scope + project select (projects from /api/projects), optional
// model / tools chips / boundaries → POST /api/system/agents → the parent
// navigates into the new detail. A 409 duplicate carrying restore_id (a
// soft-deleted twin exists) offers restore instead — creating over it is not
// possible, by design. 403 readonly bubbles to the page banner via onReadonly.

import { useEffect, useState } from 'react';
import type { Project, SystemCreateAgentRequest } from '../../api/types';
import { fetchProjects } from '../../api';
import { createSystemAgent, restoreSystemAgent, SystemWriteError } from '../../api/system';

/** step-11 server rule, enforced client-side too: lowercase kebab-case. */
const KEBAB = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

const FIELD =
  'w-full rounded-lg border border-line bg-bg px-3 py-1.5 font-mono text-[11.5px] text-ink-2 focus:border-brand focus:outline-none';
const LABEL = 'mb-1 block font-mono text-[10.5px] tracking-[0.08em] text-ink-dim uppercase';

export function CreateAgentForm({
  onCreated,
  onCancel,
  onReadonly,
}: {
  /** POST landed (or the soft-deleted twin was restored) — open the detail. */
  onCreated: (id: number) => void;
  onCancel: () => void;
  /** A write hit the global readonly kill-switch — page-level banner. */
  onReadonly: () => void;
}): JSX.Element {
  const [name, setName] = useState('');
  const [scope, setScope] = useState<'global' | 'project'>('global');
  const [projectId, setProjectId] = useState<number | null>(null);
  const [description, setDescription] = useState('');
  const [model, setModel] = useState('');
  const [tools, setTools] = useState<string[]>([]);
  const [toolInput, setToolInput] = useState('');
  const [boundaries, setBoundaries] = useState('');
  const [projects, setProjects] = useState<Project[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [restoreId, setRestoreId] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetchProjects()
      .then((rows) => {
        if (!cancelled) setProjects(rows.filter((p) => !p.archived));
      })
      .catch(() => {
        // project select stays empty — global creation still works
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const nameInvalid = name !== '' && !KEBAB.test(name);
  const canSubmit =
    !busy &&
    name !== '' &&
    !nameInvalid &&
    description.trim() !== '' &&
    (scope === 'global' || projectId !== null);

  const addTool = (): void => {
    const t = toolInput.trim().replace(/,+$/, '');
    if (t !== '' && !tools.includes(t)) setTools((prev) => [...prev, t]);
    setToolInput('');
  };

  const submit = (): void => {
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    setRestoreId(null);
    const req: SystemCreateAgentRequest = { name, scope, description: description.trim() };
    if (scope === 'project' && projectId !== null) req.project_id = projectId;
    const m = model.trim();
    if (m !== '') req.model = m;
    if (tools.length > 0) req.tools = tools;
    if (boundaries.trim() !== '') req.boundaries = boundaries;
    createSystemAgent(req)
      .then((res) => {
        onCreated(res.id);
      })
      .catch((e: unknown) => {
        if (e instanceof SystemWriteError) {
          if (e.forbidden === 'readonly') onReadonly();
          setRestoreId(e.restoreId);
          setError(e.message);
        } else {
          setError(String(e));
        }
      })
      .finally(() => setBusy(false));
  };

  const doRestore = (): void => {
    if (restoreId === null) return;
    setBusy(true);
    setError(null);
    restoreSystemAgent(restoreId)
      .then((res) => {
        onCreated(res.id);
      })
      .catch((e: unknown) => {
        if (e instanceof SystemWriteError && e.forbidden === 'readonly') onReadonly();
        setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => setBusy(false));
  };

  return (
    <div className="rounded-xl border border-line bg-surface px-4 py-3.5">
      <div className="font-display text-[14px] font-bold text-ink">New agent</div>

      <div className="mt-3 grid gap-3 desk:grid-cols-2">
        <div>
          <label className={LABEL} htmlFor="na-name">
            name
          </label>
          <input
            id="na-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="my-agent"
            spellCheck={false}
            className={FIELD}
          />
          {nameInvalid && (
            <div className="mt-1 font-mono text-[10.5px] text-red" role="alert">
              kebab-case only: lowercase letters, digits, single dashes (my-agent)
            </div>
          )}
        </div>
        <div className="flex gap-2">
          <div className="min-w-0 flex-1">
            <label className={LABEL} htmlFor="na-scope">
              scope
            </label>
            <select
              id="na-scope"
              value={scope}
              onChange={(e) => setScope(e.target.value === 'project' ? 'project' : 'global')}
              className={FIELD}
            >
              <option value="global">global</option>
              <option value="project">project</option>
            </select>
          </div>
          {scope === 'project' && (
            <div className="min-w-0 flex-1">
              <label className={LABEL} htmlFor="na-project">
                project
              </label>
              <select
                id="na-project"
                value={projectId === null ? '' : String(projectId)}
                onChange={(e) =>
                  setProjectId(e.target.value === '' ? null : Number(e.target.value))
                }
                className={FIELD}
              >
                <option value="">— pick —</option>
                {projects.map((p) => (
                  <option key={p.id} value={String(p.id)}>
                    {p.slug}
                  </option>
                ))}
              </select>
            </div>
          )}
        </div>
      </div>

      <div className="mt-3">
        <label className={LABEL} htmlFor="na-desc">
          description
        </label>
        <input
          id="na-desc"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="what this agent owns, in one line"
          className={FIELD}
        />
      </div>

      <div className="mt-3 grid gap-3 desk:grid-cols-2">
        <div>
          <label className={LABEL} htmlFor="na-model">
            model <span className="normal-case">(optional)</span>
          </label>
          <input
            id="na-model"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder="inherit from session"
            spellCheck={false}
            className={FIELD}
          />
        </div>
        <div>
          <label className={LABEL} htmlFor="na-tools">
            tools <span className="normal-case">(optional)</span>
          </label>
          <div className="flex flex-wrap items-center gap-1.5">
            {tools.map((t) => (
              <span
                key={t}
                className="flex items-center gap-1 rounded-full border border-line px-2 py-px font-mono text-[10.5px] text-ink-2"
              >
                {t}
                <button
                  type="button"
                  onClick={() => setTools((prev) => prev.filter((x) => x !== t))}
                  aria-label={`remove tool ${t}`}
                  className="text-ink-dim transition-colors hover:text-red"
                >
                  ×
                </button>
              </span>
            ))}
            <input
              id="na-tools"
              value={toolInput}
              onChange={(e) => setToolInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ',') {
                  e.preventDefault();
                  addTool();
                }
              }}
              onBlur={addTool}
              placeholder="Read, Grep… Enter adds"
              spellCheck={false}
              className="min-w-[130px] flex-1 rounded-lg border border-line bg-bg px-3 py-1.5 font-mono text-[11.5px] text-ink-2 focus:border-brand focus:outline-none"
            />
          </div>
        </div>
      </div>

      <div className="mt-3">
        <label className={LABEL} htmlFor="na-bounds">
          boundaries
        </label>
        <textarea
          id="na-bounds"
          value={boundaries}
          onChange={(e) => setBoundaries(e.target.value)}
          rows={3}
          placeholder={'- never edits code outside its scope\n- escalates when a plan assumption breaks'}
          spellCheck={false}
          className="w-full resize-y rounded-lg border border-line bg-bg px-3 py-2 font-mono text-[11.5px] leading-relaxed text-ink-2 focus:border-brand focus:outline-none"
        />
      </div>

      {error !== null && (
        <div className="mt-2 rounded-lg border border-red/25 bg-red/5 px-3 py-2" role="alert">
          <div className="font-mono text-[11.5px] text-red">{error}</div>
          {restoreId !== null && (
            <button
              type="button"
              onClick={doRestore}
              disabled={busy}
              className="mt-2 rounded-lg border border-green/40 bg-green/10 px-3 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
            >
              restore the soft-deleted agent instead
            </button>
          )}
        </div>
      )}

      <div className="mt-3.5 flex flex-wrap justify-end gap-2">
        <button
          type="button"
          onClick={onCancel}
          disabled={busy}
          className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
        >
          cancel
        </button>
        <button
          type="button"
          onClick={submit}
          disabled={!canSubmit}
          className="rounded-lg border border-green/40 bg-green/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors enabled:hover:bg-green/20 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {busy ? 'creating…' : 'create agent'}
        </button>
      </div>
    </div>
  );
}
