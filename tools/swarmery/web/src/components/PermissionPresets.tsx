// Permissions section (/p/:slug/settings — fusion phase 11): a project's
// permission preset (unrestricted | approval-required | locked-down) plus
// per-category overrides, compiled into managed auto-approve rules server-side.
// Escalating to unrestricted or promoting command_exec/git_push to 'allow' is
// gated behind a confirm dialog (the server returns 428 with the escalation
// list; we surface it and retry with confirm:true).

import { useCallback, useEffect, useRef, useState } from 'react';
import type {
  CategoryPolicy,
  PermissionPreset,
  PermissionPresetView,
} from '../api/types';
import {
  EscalationRequiredError,
  fetchPermissionPreset,
  putPermissionPreset,
} from '../api';
import { Card, ConfirmDialog, ErrorBox, Loading, SectionTitle } from './ui';

/** Plain-language copy for each preset card. */
const PRESET_CARDS: readonly {
  value: PermissionPreset;
  title: string;
  blurb: string;
}[] = [
  {
    value: 'approval-required',
    title: 'Approval required',
    blurb: 'Every tool call waits for you. The safe default — nothing runs unattended.',
  },
  {
    value: 'unrestricted',
    title: 'Unrestricted',
    blurb:
      'Auto-approve file writes, safe git, and shell by default (never pushing). Fast, but hands-off.',
  },
  {
    value: 'locked-down',
    title: 'Locked down',
    blurb: 'No auto-approval AND the dispatcher refuses to run this project’s queued tasks.',
  },
];

/** Human labels for the category rows. */
const CATEGORY_LABEL: Record<string, string> = {
  read_only: 'Read-only (read, grep, git status/log/diff)',
  file_write: 'File writes (edit, write, notebooks)',
  git_write: 'Git writes (add, commit, checkout, worktree)',
  git_push: 'Git push & gh',
  command_exec: 'Shell commands (any Bash)',
  network: 'Network (web fetch/search)',
};

/** A staged edit — the view plus any not-yet-saved override deltas. */
interface Draft {
  preset: PermissionPreset;
  overrides: Record<string, CategoryPolicy>;
}

function PresetCard({
  card,
  selected,
  disabled,
  onSelect,
}: {
  card: (typeof PRESET_CARDS)[number];
  selected: boolean;
  disabled: boolean;
  onSelect: () => void;
}): JSX.Element {
  return (
    <label
      className={`flex cursor-pointer gap-2.5 rounded-xl border px-3.5 py-3 transition-colors ${
        selected ? 'border-brand/50 bg-brand/5' : 'border-line hover:bg-surface2'
      } ${disabled ? 'cursor-not-allowed opacity-60' : ''}`}
    >
      <input
        type="radio"
        name="permission-preset"
        checked={selected}
        disabled={disabled}
        onChange={onSelect}
        className="mt-0.5 accent-brand focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
      />
      <span className="min-w-0">
        <span className="block font-mono text-[12px] font-semibold text-ink">{card.title}</span>
        <span className="mt-0.5 block text-[11.5px] leading-snug text-ink-dim">{card.blurb}</span>
      </span>
    </label>
  );
}

function CategoryRow({
  category,
  patterns,
  policy,
  editable,
  onToggle,
}: {
  category: string;
  patterns: string[];
  policy: CategoryPolicy;
  /** Overrides are only meaningful under the unrestricted preset. */
  editable: boolean;
  onToggle: () => void;
}): JSX.Element {
  const allow = policy === 'allow';
  return (
    <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 py-2 first:pt-0 last:pb-0">
      <span className="min-w-0 basis-[220px] text-[11.5px] text-ink-2">
        {CATEGORY_LABEL[category] ?? category}
      </span>
      <code className="min-w-0 flex-1 basis-[160px] truncate font-mono text-[10px] text-ink-faint">
        {patterns.join(' ')}
      </code>
      {editable ? (
        <button
          type="button"
          onClick={onToggle}
          aria-pressed={allow}
          aria-label={`${category}: ${allow ? 'auto-approve' : 'ask'}`}
          className={`ml-auto rounded-full border px-2.5 py-0.5 font-mono text-[10px] transition-colors ${
            allow
              ? 'border-green/45 bg-green/10 text-green hover:bg-green/20'
              : 'border-line text-ink-dim hover:bg-surface2'
          }`}
        >
          {allow ? 'auto-approve' : 'ask'}
        </button>
      ) : (
        <span
          className={`ml-auto rounded-full border px-2.5 py-0.5 font-mono text-[10px] ${
            allow ? 'border-green/40 text-green' : 'border-line text-ink-faint'
          }`}
        >
          {allow ? 'auto-approve' : 'ask'}
        </span>
      )}
    </div>
  );
}

export function PermissionPresets({ projectId }: { projectId: number }): JSX.Element {
  const [view, setView] = useState<PermissionPresetView | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Escalation confirm dialog: the pending draft + the reasons the server gave.
  const [escalation, setEscalation] = useState<{ draft: Draft; reasons: string[] } | null>(null);
  const aliveRef = useRef(true);

  const load = useCallback((): void => {
    fetchPermissionPreset(projectId)
      .then((v) => {
        if (!aliveRef.current) return;
        setView(v);
        setDraft({ preset: v.preset, overrides: { ...v.overrides } });
        setError(null);
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return;
        setError(e instanceof Error ? e.message : String(e));
      });
  }, [projectId]);

  useEffect(() => {
    aliveRef.current = true;
    load();
    return () => {
      aliveRef.current = false;
    };
  }, [load]);

  // Effective policy for the category table: the SAVED view when the draft
  // matches it, else a client-side preview of the staged draft (unrestricted
  // shows overrides live; other presets force every category to ask).
  const effectivePolicyOf = (category: string): CategoryPolicy => {
    if (draft === null || view === null) return 'ask';
    if (draft.preset !== 'unrestricted') return 'ask';
    const override = draft.overrides[category];
    if (override !== undefined) return override;
    // Fall back to the server's baseline for this category (from the last view).
    const base = view.categories.find((c) => c.category === category);
    // The unrestricted default = allow for everything except git_push.
    return base && draft.preset === view.preset ? base.policy : category === 'git_push' ? 'ask' : 'allow';
  };

  const dirty =
    draft !== null &&
    view !== null &&
    (draft.preset !== view.preset ||
      JSON.stringify(draft.overrides) !== JSON.stringify(view.overrides));

  const selectPreset = (preset: PermissionPreset): void => {
    setDraft((d) => (d === null ? d : { ...d, preset }));
  };

  const toggleCategory = (category: string): void => {
    setDraft((d) => {
      if (d === null) return d;
      const current = effectivePolicyOf(category);
      const next: CategoryPolicy = current === 'allow' ? 'ask' : 'allow';
      return { ...d, overrides: { ...d.overrides, [category]: next } };
    });
  };

  // Persist the draft. On a 428 the server is telling us the change is
  // privileged — stash it and open the confirm dialog; the confirm path resends
  // with confirm:true.
  const save = (confirm: boolean, toSave?: Draft): void => {
    const payload = toSave ?? draft;
    if (payload === null) return;
    setBusy(true);
    putPermissionPreset(projectId, {
      preset: payload.preset,
      overrides: payload.overrides,
      confirm,
    })
      .then((v) => {
        if (!aliveRef.current) return;
        setView(v);
        setDraft({ preset: v.preset, overrides: { ...v.overrides } });
        setEscalation(null);
        setError(null);
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return;
        if (e instanceof EscalationRequiredError) {
          setEscalation({ draft: payload, reasons: e.escalations });
          return;
        }
        setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (aliveRef.current) setBusy(false);
      });
  };

  const reset = (): void => {
    if (view === null) return;
    setDraft({ preset: view.preset, overrides: { ...view.overrides } });
    setError(null);
  };

  if (error !== null && view === null) {
    return (
      <>
        <SectionTitle>permissions</SectionTitle>
        <ErrorBox message={error} onRetry={load} />
      </>
    );
  }
  if (view === null || draft === null) {
    return (
      <>
        <SectionTitle>permissions</SectionTitle>
        <Loading label="permissions…" />
      </>
    );
  }

  const editable = draft.preset === 'unrestricted';

  return (
    <>
      <SectionTitle>permissions</SectionTitle>
      {error !== null && (
        <div className="mb-2">
          <ErrorBox message={error} onRetry={reset} />
        </div>
      )}
      <Card>
        <div className="flex flex-col gap-2">
          {PRESET_CARDS.map((card) => (
            <PresetCard
              key={card.value}
              card={card}
              selected={draft.preset === card.value}
              disabled={busy}
              onSelect={() => selectPreset(card.value)}
            />
          ))}
        </div>

        <div className="mt-4 mb-1 font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">
          Categories
        </div>
        <div className="divide-y divide-line-soft">
          {view.categories.map((c) => (
            <CategoryRow
              key={c.category}
              category={c.category}
              patterns={c.patterns}
              policy={effectivePolicyOf(c.category)}
              editable={editable && !busy}
              onToggle={() => toggleCategory(c.category)}
            />
          ))}
        </div>
        {!editable && (
          <div className="mt-2 font-mono text-[10px] text-ink-faint">
            per-category overrides apply only under the unrestricted preset
          </div>
        )}

        <div className="mt-4 flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={!dirty || busy}
            onClick={() => save(false)}
            className="rounded-lg border border-brand/45 bg-brand/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
          >
            {busy ? 'saving…' : 'save'}
          </button>
          {dirty && (
            <button
              type="button"
              disabled={busy}
              onClick={reset}
              className="rounded-lg border border-line-strong px-3 py-1.5 font-mono text-[11.5px] text-ink-3 transition-colors hover:bg-surface2 disabled:opacity-50"
            >
              reset
            </button>
          )}
          {view.lockedDown && (
            <span className="ml-auto rounded-full border border-amber/40 px-2.5 py-0.5 font-mono text-[10px] text-amber">
              dispatcher blocked
            </span>
          )}
        </div>
        <div className="mt-2 font-mono text-[10px] text-ink-faint">
          compiled into managed auto-approve rules · manual rules on the Approvals page are untouched
        </div>
      </Card>

      <ConfirmDialog
        open={escalation !== null}
        title="Confirm broader permissions"
        confirmLabel="I understand — apply"
        danger
        busy={busy}
        onConfirm={() => {
          if (escalation !== null) save(true, escalation.draft);
        }}
        onCancel={() => setEscalation(null)}
      >
        <p>{'This grants agents more autonomy for this project:'}</p>
        <ul className="mt-2 list-disc pl-5">
          {(escalation?.reasons ?? []).map((reason) => (
            <li key={reason} className="font-mono text-[11.5px] text-ink">
              {reason}
            </li>
          ))}
        </ul>
        <p className="mt-2 text-ink-dim">Git push is never auto-approved unless you enable it explicitly.</p>
      </ConfirmDialog>
    </>
  );
}
