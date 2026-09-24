// Plugin config modal — the operator-facing form for a pack's declared
// .claude/project.json key (requirements.json → projectConfig[]). The form is
// GENERATED from the pack's own JSON Schema fragment (row.configSchema):
// string leaves, integer leaves (minimum + default), and object nesting into
// fieldsets. Nothing about any specific pack's vocabulary is hardcoded here —
// that is the whole point of the contract (Phase 1's requirements.json).
//
// `default` renders as a placeholder ONLY — a value the operator never typed
// is never silently saved. Prefill comes solely from `configCurrent`. Server
// 422 `problems` (one line per dotted field path, e.g. "child.leaf is
// required") are mapped back onto their field by prefix match; anything that
// doesn't match a rendered leaf (e.g. "unknown field: xyz") falls through to
// the general ErrorBox — same shape as AttachModal/DetachModal.

import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { PluginConfigProbe, ProjectPluginRow } from '../api/types';
import { ConfigValidationError, probeProjectConfig, putProjectConfig } from '../api';
import { ConfirmDialog, ErrorBox } from './ui';

/** The subset of JSON Schema the contract allows (Phase 1): string / integer
 * leaves, object nesting, required + default + minimum. Everything else on
 * the wire is ignored. */
interface SchemaNode {
  type?: string;
  description?: string;
  properties?: Record<string, SchemaNode>;
  required?: string[];
  default?: unknown;
  minimum?: number;
  maximum?: number;
  enum?: unknown[];
}

/** A leaf whose value must reach project.json as a JSON number, not a string. */
function isNumeric(schema: SchemaNode): boolean {
  return schema.type === 'integer' || schema.type === 'number';
}

/** A closed set of allowed values — rendered as a select, because a free-text
 * box over an enum asks the operator to guess a vocabulary the form is holding
 * and the daemon's validator does not enforce. */
function enumOptions(schema: SchemaNode): string[] | null {
  if (!Array.isArray(schema.enum) || schema.enum.length === 0) return null;
  return schema.enum.filter((v): v is string => typeof v === 'string');
}

type FormValue = Record<string, unknown>;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function isObjectSchema(schema: SchemaNode): schema is SchemaNode & { properties: Record<string, SchemaNode> } {
  return schema.type === 'object' && schema.properties !== undefined;
}

/** Immutable set at a dotted path — intermediate objects are shallow-cloned. */
function setAt(obj: FormValue, path: string[], value: unknown): FormValue {
  const [head, ...rest] = path;
  if (head === undefined) return obj;
  const next: FormValue = { ...obj };
  if (rest.length === 0) {
    next[head] = value;
  } else {
    const child = isRecord(obj[head]) ? (obj[head] as FormValue) : {};
    next[head] = setAt(child, rest, value);
  }
  return next;
}

/** True when a value (leaf or nested object) has nothing meaningful in it —
 * an untouched optional object must never be submitted as `{}`. */
function isEmptyValue(value: unknown): boolean {
  if (value === undefined || value === null || value === '') return true;
  if (isRecord(value)) return Object.values(value).every(isEmptyValue);
  return false;
}

/** Builds the JSON value to submit: typed leaves, empty optional leaves and
 * empty optional objects OMITTED entirely (never a silently-saved default). */
export function buildSubmitValue(schema: SchemaNode, value: unknown): unknown {
  if (isObjectSchema(schema)) {
    const out: FormValue = {};
    for (const [key, child] of Object.entries(schema.properties)) {
      const childValue = isRecord(value) ? value[key] : undefined;
      if (isEmptyValue(childValue)) continue;
      out[key] = buildSubmitValue(child, childValue);
    }
    return out;
  }
  if (isNumeric(schema)) {
    if (typeof value === 'number') return value;
    if (typeof value === 'string' && value.trim() !== '') {
      const n = Number(value);
      if (Number.isFinite(n)) return n;
    }
    return undefined;
  }
  return value;
}

/** Client-side required check, dotted-path keyed — same shape as the
 * server's `problems`, so client and server errors render identically. */
function validateRequired(schema: SchemaNode, value: unknown, prefix: string[] = []): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!isObjectSchema(schema)) return errors;
  const required = schema.required ?? [];
  for (const [key, child] of Object.entries(schema.properties)) {
    const path = [...prefix, key];
    const childValue = isRecord(value) ? value[key] : undefined;
    if (isObjectSchema(child)) {
      Object.assign(errors, validateRequired(child, childValue, path));
    } else if (required.includes(key) && isEmptyValue(childValue)) {
      errors[path.join('.')] = `${path.join('.')} is required`;
    }
  }
  return errors;
}

/** First leaf field's dotted path, in schema property order — the field that
 * gets `autoFocus` on open, computed once regardless of nesting depth. */
function firstLeafPath(schema: SchemaNode, prefix: string[] = []): string | null {
  if (!isObjectSchema(schema)) return null;
  for (const [key, child] of Object.entries(schema.properties)) {
    const path = [...prefix, key];
    if (isObjectSchema(child)) {
      const nested = firstLeafPath(child, path);
      if (nested !== null) return nested;
    } else {
      return path.join('.');
    }
  }
  return null;
}

/** Every rendered leaf's dotted path, longest first — used to place a server
 * problem line by PREFIX match, since the server sends a human sentence
 * ("child.leaf is required"), not a machine field pointer. Longest-first so
 * "child.leaf" wins over "child" for the same line. */
function leafPaths(schema: SchemaNode, prefix: string[] = []): string[] {
  if (!isObjectSchema(schema)) return [];
  const out: string[] = [];
  for (const [key, child] of Object.entries(schema.properties)) {
    const path = [...prefix, key];
    if (isObjectSchema(child)) out.push(...leafPaths(child, path));
    else out.push(path.join('.'));
  }
  return out.sort((a, b) => b.length - a.length);
}

/** Reads a dotted path out of the form value — the same addressing the server
 * uses for `needs`, `fields`, and its `problems` lines. */
function valueAt(value: unknown, dotted: string): unknown {
  let cur: unknown = value;
  for (const segment of dotted.split('.')) {
    if (!isRecord(cur)) return undefined;
    cur = cur[segment];
  }
  return cur;
}

/** The probe's own inputs that are still empty. Empty result = it can run.
 *
 * `needs` is optional in a pack's requirements.json, and a daemon older than
 * the jsonList fix serves the omission as null — hence the guard, which keeps
 * this modal open against a daemon this build did not ship with. */
function unfilledNeeds(probe: PluginConfigProbe, value: FormValue): string[] {
  return (probe.needs ?? []).filter((path) => isEmptyValue(valueAt(value, path)));
}

/**
 * Writes each field's FIRST candidate into the form — but only into fields that
 * are still empty.
 *
 * The probe already knows the answers; leaving them in a datalist made the
 * operator retype what the agent just read off their own repo. Filling is
 * therefore the default, and the review gate stays exactly where it was: the
 * form is populated, `save` is still a deliberate press, and every filled field
 * says where its value came from.
 *
 * Never an overwrite. A value the operator typed, and a value already saved in
 * project.json, both outrank a suggestion — the probe is one read of a
 * repository, and the operator may know something it could not see. Only paths
 * the schema actually renders are touched, so a pack that nominates a field it
 * no longer declares cannot smuggle a key past the form.
 */
export function fillEmptyFrom(
  value: FormValue,
  suggestions: Record<string, string[]>,
  knownLeaves: string[],
): { next: FormValue; filled: string[] } {
  let next = value;
  const filled: string[] = [];
  for (const dotted of knownLeaves) {
    const candidate = suggestions[dotted]?.[0];
    if (candidate === undefined || candidate === '') continue;
    if (!isEmptyValue(valueAt(next, dotted))) continue;
    next = setAt(next, dotted.split('.'), candidate);
    filled.push(dotted);
  }
  return { next, filled };
}

type Phase = { kind: 'editing' } | { kind: 'saving' };

/** The probe is a side channel, never a gate: `idle` and `running` both leave
 * the form fully usable, and `done` only adds suggestions or a grey line. */
type ProbePhase =
  | { kind: 'idle' }
  | { kind: 'running' }
  | { kind: 'done'; suggestions: Record<string, string[]>; reason?: string | undefined };

export function PluginConfigModal({
  projectId,
  row,
  onClose,
  onSaved,
}: {
  projectId: number;
  row: ProjectPluginRow;
  onClose: () => void;
  onSaved: () => void;
}): JSX.Element {
  const titleId = useId();
  const schema = row.configSchema as SchemaNode | undefined;
  const [value, setValue] = useState<FormValue>(
    isRecord(row.configCurrent) ? (row.configCurrent as FormValue) : {},
  );
  const initialValueRef = useRef(value);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [generalError, setGeneralError] = useState<string | null>(null);
  const [phase, setPhase] = useState<Phase>({ kind: 'editing' });
  const busy = phase.kind === 'saving';
  const dirty = JSON.stringify(value) !== JSON.stringify(initialValueRef.current);

  function requestClose(): void {
    if (busy) return;
    if (dirty) {
      setConfirmDiscard(true);
      return;
    }
    onClose();
  }
  const firstPath = useMemo(() => (schema !== undefined ? firstLeafPath(schema) : null), [schema]);
  const knownLeaves = useMemo(() => (schema !== undefined ? leafPaths(schema) : []), [schema]);
  const filledCount = knownLeaves.filter((dotted) => !isEmptyValue(valueAt(value, dotted))).length;
  const errorCount = Object.keys(fieldErrors).length;
  // The scrolling field body — held so a rejected save can bring the first
  // complaint back into view.
  const bodyRef = useRef<HTMLDivElement | null>(null);

  const probe = row.configProbe;
  const [probePhase, setProbePhase] = useState<ProbePhase>({ kind: 'idle' });
  // Fields this modal filled from the probe rather than from project.json or
  // the operator's typing. Kept so each one can say so, and dropped per field
  // the moment it is edited — after that the value is the operator's.
  const [autoFilled, setAutoFilled] = useState<string[]>([]);
  // Latest form contents for the probe's late callback to merge into.
  const valueRef = useRef(value);
  valueRef.current = value;
  // Held in a ref rather than state: aborting is a side effect on an in-flight
  // request, and re-rendering the form because one was cancelled would be noise.
  const probeAbort = useRef<AbortController | null>(null);
  const probing = probePhase.kind === 'running';
  const suggestions = probePhase.kind === 'done' ? probePhase.suggestions : {};
  const probeBlockers = probe === undefined ? [] : unfilledNeeds(probe, value);
  const canProbe = probe !== undefined && probeBlockers.length === 0;

  function runProbe(): void {
    if (probe === undefined || row.configKey === undefined || schema === undefined) return;
    if (unfilledNeeds(probe, value).length > 0) return;
    probeAbort.current?.abort();
    const controller = new AbortController();
    probeAbort.current = controller;
    setProbePhase({ kind: 'running' });
    // The form's current partial contents, shaped exactly as a save would shape
    // them (empties omitted, integers as numbers) — that is the only thing the
    // daemon substitutes into the pack's prompt.
    const partial = buildSubmitValue(schema, value);
    probeProjectConfig(projectId, row.configKey, partial, controller.signal)
      .then((res) => {
        if (controller.signal.aborted) return;
        setProbePhase({ kind: 'done', suggestions: res.suggestions, reason: res.reason });
        // Read through the ref, not this closure: a probe runs for minutes and
        // the operator may have typed into the form the whole time. Filling and
        // marking are two setStates over one snapshot rather than a side effect
        // inside an updater, which React is free to run twice.
        const { next, filled } = fillEmptyFrom(valueRef.current, res.suggestions, knownLeaves);
        setValue(next);
        setAutoFilled(filled);
      })
      .catch((e: unknown) => {
        if (controller.signal.aborted) return;
        // Even the refusals land in the grey line rather than the error box:
        // nothing the operator was doing has failed, and a red banner over an
        // optional convenience would read as "the config page is broken".
        setProbePhase({ kind: 'done', suggestions: {}, reason: e instanceof Error ? e.message : String(e) });
      });
  }

  function skipProbe(): void {
    probeAbort.current?.abort();
    probeAbort.current = null;
    setProbePhase({ kind: 'idle' });
  }

  // Auto-run ON OPEN when the inputs are already there — the repeat-visit case,
  // where the operator came back precisely to fix the one field the probe can
  // answer for.
  //
  // Mount-only, deliberately. Keying this on "needs are filled" instead would
  // fire the moment the last input became non-empty — i.e. mid-keystroke, on a
  // half-typed project key — and spend a three-minute agent run on it. Once the
  // operator is typing, starting the probe is their call: that is the button.
  useEffect(() => {
    runProbe();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // A field that fails validation may now be scrolled out of sight, so a
  // rejected save has to go and fetch it — otherwise the press reads as a modal
  // that did nothing. Runs after the errors have rendered, hence the effect.
  useEffect(() => {
    if (errorCount === 0) return;
    bodyRef.current?.querySelector('[aria-invalid="true"]')?.scrollIntoView({ block: 'center' });
  }, [fieldErrors, errorCount]);

  // An unmount mid-probe must cancel the request: the daemon's timeout hangs off
  // the request context, so aborting kills the agent process instead of leaving
  // it running for minutes against a modal nobody is looking at.
  useEffect(() => () => probeAbort.current?.abort(), []);

  function onKeyDown(e: React.KeyboardEvent<HTMLDivElement>): void {
    if (e.key !== 'Escape' || busy) return;
    if (confirmDiscard) {
      setConfirmDiscard(false);
      return;
    }
    requestClose();
  }

  async function save(): Promise<void> {
    if (schema === undefined || row.configKey === undefined) return;
    const errors = validateRequired(schema, value);
    if (Object.keys(errors).length > 0) {
      setFieldErrors(errors);
      setGeneralError(null);
      return;
    }
    setPhase({ kind: 'saving' });
    setFieldErrors({});
    setGeneralError(null);
    const submitValue = buildSubmitValue(schema, value);
    try {
      await putProjectConfig(projectId, row.configKey, submitValue);
      onSaved();
    } catch (e) {
      setPhase({ kind: 'editing' });
      if (e instanceof ConfigValidationError) {
        const byField: Record<string, string> = {};
        const unmatched: string[] = [];
        for (const problem of e.problems) {
          const field = knownLeaves.find((p) => problem.startsWith(p));
          if (field === undefined) {
            unmatched.push(problem);
            continue;
          }
          // Accumulate rather than overwrite: the server may report more than
          // one problem for the same leaf, and dropping the earlier one would
          // silently hide a validation failure the operator has to fix.
          const prior = byField[field];
          byField[field] = prior === undefined ? problem : `${prior}; ${problem}`;
        }
        setFieldErrors(byField);
        setGeneralError(unmatched.length > 0 ? unmatched.join('; ') : null);
      } else {
        setGeneralError(e instanceof Error ? e.message : String(e));
      }
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      onClick={busy ? undefined : requestClose}
      onKeyDown={onKeyDown}
    >
      <div
        className="flex max-h-[85vh] w-full max-w-lg flex-col rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="shrink-0">
          <div id={titleId} className="font-display text-[14px] font-bold text-ink">
            {row.configTitle ?? row.name}
          </div>
          {row.configWhy !== undefined && row.configWhy !== '' && (
            <div className="mt-1 text-[12px] leading-relaxed text-ink-dim">{row.configWhy}</div>
          )}
          {row.configDocs !== undefined && row.configDocs !== '' && (
            <div className="mt-1.5 font-mono text-[10.5px] text-ink-faint">docs: {row.configDocs}</div>
          )}
        </div>

        {schema === undefined || !isObjectSchema(schema) || row.configKey === undefined ? (
          <ErrorBox message="this pack's config schema is missing or malformed" />
        ) : (
          <form
            className="mt-3 flex min-h-0 flex-1 flex-col"
            onSubmit={(e) => {
              e.preventDefault();
              void save();
            }}
          >
            {/* Only the fields scroll. A pack declares as many as it needs —
                design-pack alone renders twelve across three fieldsets — and a
                form taller than the viewport used to push `save` off-screen with
                no way to reach it. Header and footer stay put; the count below
                says how much is out of sight. */}
            <div ref={bodyRef} className="min-h-0 flex-1 overflow-y-auto pr-1">
              <SchemaFields
                schema={schema}
                path={[]}
                value={value}
                onChange={(path, v) => {
                  setValue((cur) => setAt(cur, path, v));
                  // Editing a filled field ends its provenance: from here the
                  // value is the operator's, whatever the probe had said.
                  const dotted = path.join('.');
                  setAutoFilled((cur) => (cur.includes(dotted) ? cur.filter((p) => p !== dotted) : cur));
                }}
                fieldErrors={fieldErrors}
                firstPath={firstPath}
                suggestions={suggestions}
                autoFilled={autoFilled}
              />
            </div>

            <div className="mt-3 shrink-0 border-t border-line-soft">
              {probe !== undefined && (
                <ProbeRow
                  probing={probing}
                  canProbe={canProbe}
                  blockers={probeBlockers}
                  reason={probePhase.kind === 'done' ? probePhase.reason : undefined}
                  found={Object.keys(suggestions).length}
                  filled={autoFilled.length}
                  onRun={runProbe}
                  onSkip={skipProbe}
                />
              )}

              {generalError !== null && <ErrorBox message={generalError} />}

              <div className="mt-3 flex items-center justify-between gap-2">
                {/* The one thing a scrolled form hides: how many fields are left
                    and whether any of them is complaining. */}
                <span className="font-mono text-[10px] text-ink-faint">
                  {errorCount > 0 ? (
                    <span className="text-red">
                      {errorCount} field{errorCount === 1 ? '' : 's'} need attention
                    </span>
                  ) : (
                    `${filledCount}/${knownLeaves.length} fields`
                  )}
                </span>
                <span className="flex gap-2">
                  <button
                    type="button"
                    onClick={requestClose}
                    disabled={busy}
                    className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
                  >
                    cancel
                  </button>
                  <button
                    type="submit"
                    disabled={busy}
                    className="rounded-lg border border-green/40 bg-green/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
                  >
                    {busy ? '…' : 'save'}
                  </button>
                </span>
              </div>
            </div>
          </form>
        )}

        {(schema === undefined || !isObjectSchema(schema) || row.configKey === undefined) && (
          <div className="mt-4 flex justify-end">
            <button
              type="button"
              onClick={onClose}
              className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2"
            >
              close
            </button>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmDiscard}
        title="Discard changes?"
        confirmLabel="discard"
        danger
        onConfirm={onClose}
        onCancel={() => setConfirmDiscard(false)}
      >
        Your edits to this plugin's configuration will be lost.
      </ConfirmDialog>
    </div>
  );
}

/**
 * The probe's own line: a button while it can run, `probing… / skip` while it
 * does, and a grey outcome afterwards.
 *
 * Nothing here disables anything else. The probe is a convenience layered over
 * a form that already worked without it, so a run in flight must never take the
 * inputs, `save`, or `cancel` away from the operator.
 */
function ProbeRow({
  probing,
  canProbe,
  blockers,
  reason,
  found,
  filled,
  onRun,
  onSkip,
}: {
  probing: boolean;
  canProbe: boolean;
  blockers: string[];
  reason: string | undefined;
  found: number;
  /** How many empty fields the probe's answers were written into. */
  filled: number;
  onRun: () => void;
  onSkip: () => void;
}): JSX.Element {
  if (probing) {
    return (
      <div className="mt-3 flex items-center gap-2 font-mono text-[10.5px] text-ink-faint">
        <span aria-live="polite">probing for live values…</span>
        <button
          type="button"
          onClick={onSkip}
          className="rounded border border-line px-1.5 py-0.5 text-ink-dim transition-colors hover:bg-surface2"
        >
          skip
        </button>
      </div>
    );
  }
  return (
    <div className="mt-3 flex items-center gap-2 font-mono text-[10.5px] text-ink-faint">
      <button
        type="button"
        onClick={onRun}
        disabled={!canProbe}
        title={canProbe ? undefined : `fill in ${blockers.join(', ')} first`}
        className="rounded border border-line px-1.5 py-0.5 text-ink-dim transition-colors hover:bg-surface2 disabled:opacity-40"
      >
        probe
      </button>
      {!canProbe && <span>needs {blockers.join(', ')}</span>}
      {canProbe && reason !== undefined && reason !== '' && <span>probe: {reason}</span>}
      {canProbe && reason === undefined && found > 0 && (
        // Filled, not merely suggested — and the sentence says so, because a
        // form that populated itself must not read as one the operator filled.
        <span>
          {filled > 0
            ? `filled ${filled} field${filled === 1 ? '' : 's'} from this project — review, then save`
            : `suggested values for ${found} field${found === 1 ? '' : 's'}`}
        </span>
      )}
    </div>
  );
}

function SchemaFields({
  schema,
  path,
  value,
  onChange,
  fieldErrors,
  firstPath,
  suggestions,
  autoFilled,
}: {
  schema: SchemaNode & { properties: Record<string, SchemaNode> };
  path: string[];
  value: unknown;
  onChange: (path: string[], value: unknown) => void;
  fieldErrors: Record<string, string>;
  firstPath: string | null;
  suggestions: Record<string, string[]>;
  /** Dotted paths this modal filled from the probe — rendered as provenance. */
  autoFilled: string[];
}): JSX.Element {
  const required = schema.required ?? [];
  return (
    <div className="space-y-2.5">
      {Object.entries(schema.properties).map(([key, child]) => {
        const childPath = [...path, key];
        const dotted = childPath.join('.');
        const childValue = isRecord(value) ? value[key] : undefined;
        const isReq = required.includes(key);
        if (isObjectSchema(child)) {
          return (
            <fieldset key={dotted} className="rounded-lg border border-line-soft px-3 py-2.5">
              <legend className="px-1 font-mono text-[10px] tracking-[0.12em] text-ink-dim uppercase">
                {key}
                {isReq ? <span className="text-red"> *</span> : null}
              </legend>
              {child.description !== undefined && child.description !== '' && (
                <p className="mb-2 text-[11px] text-ink-faint">{child.description}</p>
              )}
              <SchemaFields
                schema={child}
                path={childPath}
                value={childValue}
                onChange={onChange}
                fieldErrors={fieldErrors}
                firstPath={firstPath}
                suggestions={suggestions}
                autoFilled={autoFilled}
              />
            </fieldset>
          );
        }
        return (
          <LeafField
            key={dotted}
            fieldKey={key}
            path={childPath}
            schema={child}
            value={childValue}
            required={isReq}
            error={fieldErrors[dotted]}
            autoFocus={dotted === firstPath}
            onChange={onChange}
            suggestions={suggestions[dotted]}
            autoFilled={autoFilled.includes(dotted)}
          />
        );
      })}
    </div>
  );
}

function LeafField({
  fieldKey,
  path,
  schema,
  value,
  required,
  error,
  autoFocus,
  onChange,
  suggestions,
  autoFilled,
}: {
  fieldKey: string;
  path: string[];
  schema: SchemaNode;
  value: unknown;
  required: boolean;
  error: string | undefined;
  autoFocus: boolean;
  onChange: (path: string[], value: unknown) => void;
  suggestions: string[] | undefined;
  /** This field's value came from the probe and has not been edited since. */
  autoFilled: boolean;
}): JSX.Element {
  const dotted = path.join('.');
  const fieldId = `cfg-${dotted.replace(/\./g, '-')}`;
  const hintId = `${fieldId}-hint`;
  const errorId = `${fieldId}-error`;
  const listId = `${fieldId}-suggestions`;
  const numeric = isNumeric(schema);
  const options = enumOptions(schema);
  const hasDescription = schema.description !== undefined && schema.description !== '';
  const hasSuggestions = suggestions !== undefined && suggestions.length > 0;
  const describedBy = [hasDescription ? hintId : null, error !== undefined ? errorId : null]
    .filter((v): v is string => v !== null)
    .join(' ');

  return (
    <div>
      <label htmlFor={fieldId} className="block font-mono text-[10.5px] tracking-[0.12em] text-ink-dim uppercase">
        {fieldKey}
        {required ? <span className="text-red"> *</span> : null}
      </label>
      {options !== null ? (
        // A select only where the SCHEMA closed the set. Everywhere else the box
        // stays free text — see the datalist note below.
        <select
          id={fieldId}
          value={typeof value === 'string' ? value : ''}
          autoFocus={autoFocus}
          aria-describedby={describedBy === '' ? undefined : describedBy}
          aria-invalid={error !== undefined}
          onChange={(e) => onChange(path, e.target.value)}
          className={`mt-1 w-full rounded-lg border bg-bg px-2.5 py-1.5 font-mono text-[12.5px] text-ink outline-none focus:border-line-strong ${
            error !== undefined ? 'border-red/50' : 'border-line'
          }`}
        >
          <option value="">{schema.default !== undefined ? String(schema.default) : '—'}</option>
          {options.map((option) => (
            <option key={option} value={option}>
              {option}
            </option>
          ))}
        </select>
      ) : (
        <input
          id={fieldId}
          type={numeric ? 'number' : 'text'}
          min={numeric ? schema.minimum : undefined}
          max={numeric ? schema.maximum : undefined}
          step={schema.type === 'number' ? 'any' : undefined}
          // A datalist, never a select. The probe reads what it can reach, and
          // what it reaches is not guaranteed to be the whole truth — a workflow
          // it could not see, or a script the manifest does not declare, would be
          // unreachable in a closed dropdown. Suggestions accelerate typing; they
          // never take it away.
          list={hasSuggestions ? listId : undefined}
          value={typeof value === 'string' || typeof value === 'number' ? value : ''}
          placeholder={schema.default !== undefined ? String(schema.default) : undefined}
          autoFocus={autoFocus}
          aria-describedby={describedBy === '' ? undefined : describedBy}
          aria-invalid={error !== undefined}
          onChange={(e) => onChange(path, e.target.value)}
          className={`mt-1 w-full rounded-lg border bg-bg px-2.5 py-1.5 font-mono text-[12.5px] text-ink outline-none focus:border-line-strong ${
            error !== undefined ? 'border-red/50' : 'border-line'
          }`}
        />
      )}
      {hasSuggestions && (
        <datalist id={listId}>
          {suggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      )}
      {hasDescription && (
        <p id={hintId} className="mt-1 text-[10.5px] text-ink-faint">
          {schema.description}
        </p>
      )}
      {hasSuggestions && (
        <p className="mt-1 font-mono text-[10px] text-ink-faint">
          {autoFilled
            ? `filled by probe${suggestions.length > 1 ? ` · ${suggestions.length - 1} more suggested` : ''}`
            : `${suggestions.length} suggested by probe`}
        </p>
      )}
      {error !== undefined && (
        <p id={errorId} className="mt-1 text-[10.5px] text-red">
          {error}
        </p>
      )}
    </div>
  );
}
