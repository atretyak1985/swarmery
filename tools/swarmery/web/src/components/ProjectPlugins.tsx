// Plugins section (/projects/:id, managed projects): the swarmery marketplace
// catalog with per-project enable/disable toggles. Writes edit the project's
// .claude/settings.json via the fenced PUT endpoint and take effect in the
// NEXT Claude Code session; core is locked (attach/detach owns its lifecycle).
//
// Each row also carries the daemon's drift verdict: a plugin can be enabled
// here and still not be loadable, which is invisible from settings.json alone.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { PluginDriftStatus, ProjectPluginRow, ProjectPluginsResponse } from '../api/types';
import { fetchProjectPlugins, repairProjectPlugin, toggleProjectPlugin } from '../api';
import { Card, ErrorBox, Loading, SectionTitle } from './ui';
import { PluginConfigModal } from './PluginConfigModal';

// red = the plugin is not loaded at all; amber = it loads, but stale or from a
// reclaimed cache dir. Both tokens are defined in light and dark themes.
const STATUS_STYLES: Record<Exclude<PluginDriftStatus, 'ok' | 'unknown'>, string> = {
  missing: 'border-red/40 bg-red/10 text-red',
  orphaned: 'border-amber/40 bg-amber/10 text-amber',
  behind: 'border-amber/40 bg-amber/10 text-amber',
};

// needs-config is NOT a PluginDriftStatus (see PluginConfigStatus in
// api/types.ts) — it is a separate, lower-priority chip: the daemon only ever
// sets it when drift is already 'ok' (repair outranks config), so a row shows
// at most one of the two chips, never both.
function StatusChip({ row }: { row: ProjectPluginRow }): JSX.Element | null {
  const { t } = useLingui();
  if (row.status !== 'ok' && row.status !== 'unknown') {
    return (
      <span
        data-tip={row.detail}
        className={`shrink-0 rounded-full border px-2 py-0.5 font-mono text-[10px] ${STATUS_STYLES[row.status]}`}
      >
        {row.status}
      </span>
    );
  }
  if (row.configStatus === 'needs-config') {
    const missing = (row.configMissing ?? []).join(', ');
    return (
      <span
        data-tip={missing !== '' ? t`missing: ${missing}` : undefined}
        className="shrink-0 rounded-full border border-amber/40 bg-amber/10 px-2 py-0.5 font-mono text-[10px] text-amber"
      >
        {/* i18n-ignore: the daemon's config-status code, shown as-is like the drift statuses */}
        needs-config
      </span>
    );
  }
  return null;
}

// Pill-button twin of RepairButton, but the action it drives (opening
// PluginConfigModal) has its own loading/error state, so this stays a plain
// button — 'configure' when the declared key is unfilled, 'edit' once it's
// ok (so a wrong value can be fixed without hand-editing project.json).
function ConfigureButton({
  row,
  disabled,
  onOpen,
}: {
  row: ProjectPluginRow;
  disabled: boolean;
  onOpen: () => void;
}): JSX.Element | null {
  const { t } = useLingui();
  if (row.configStatus === undefined) return null;
  const needsConfig = row.configStatus === 'needs-config';
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onOpen}
      data-tip={disabled ? t`read-only — daemon started without SWARMERY_ONBOARD_ROOTS` : undefined}
      className={`shrink-0 rounded-full border border-line px-2 py-0.5 font-mono text-[10px] transition-colors hover:text-ink disabled:cursor-not-allowed disabled:opacity-50 ${
        needsConfig ? 'text-amber' : 'text-ink-dim'
      }`}
    >
      {needsConfig ? t`configure` : t`edit`}
    </button>
  );
}

function RepairButton({
  projectId,
  row,
  marketplace,
  disabled,
  onDone,
}: {
  projectId: number;
  row: ProjectPluginRow;
  marketplace: string;
  disabled: boolean;
  onDone: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // A repair can succeed and still need the operator's attention — the
  // user-scope fallback reverts a global enable, and that revert can fail.
  const [warning, setWarning] = useState<string | null>(null);
  const run = (): void => {
    setBusy(true);
    setErr(null);
    setWarning(null);
    repairProjectPlugin(projectId, `${row.name}@${marketplace}`)
      .then((res) => {
        setWarning(res.warning ?? null);
        onDone();
      })
      .catch((e: unknown) => {
        setErr(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        setBusy(false);
      });
  };
  const label = busy ? '…' : err !== null ? t`failed` : warning !== null ? t`check` : t`repair`;
  return (
    <button
      type="button"
      disabled={disabled || busy}
      onClick={run}
      data-tip={
        err ??
        warning ??
        (disabled
          ? t`read-only — daemon started without SWARMERY_ONBOARD_ROOTS`
          : t`run claude plugin install/update for this project`)
      }
      className={`shrink-0 rounded-full border border-line px-2 py-0.5 font-mono text-[10px] transition-colors hover:text-ink disabled:cursor-not-allowed disabled:opacity-50 ${
        warning !== null ? 'text-amber' : 'text-ink-dim'
      }`}
    >
      {label}
    </button>
  );
}

function ToggleButton({
  row,
  disabled,
  busy,
  onToggle,
}: {
  row: ProjectPluginRow;
  disabled: boolean;
  busy: boolean;
  onToggle: () => void;
}): JSX.Element {
  const { t } = useLingui();
  if (row.locked) {
    return (
      <span
        className="font-mono text-[10px] text-ink-faint"
        data-tip={t`core is managed via attach/detach`}
      >
        <Trans>via attach/detach</Trans>
      </span>
    );
  }
  const name = row.name;
  return (
    <button
      type="button"
      disabled={disabled || busy}
      onClick={onToggle}
      aria-pressed={row.enabled}
      aria-label={row.enabled ? t`${name}: enabled` : t`${name}: disabled`}
      data-tip={disabled ? t`read-only — daemon started without SWARMERY_ONBOARD_ROOTS` : undefined}
      className={`rounded-full border px-2.5 py-0.5 font-mono text-[10px] transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
        row.enabled
          ? 'border-brand/40 bg-brand/10 text-brand hover:bg-brand/20'
          : 'border-line text-ink-faint hover:text-ink'
      }`}
    >
      {busy ? '…' : row.enabled ? t`on` : t`off`}
    </button>
  );
}

export function ProjectPlugins({ projectId }: { projectId: number }): JSX.Element {
  const { t } = useLingui();
  const [data, setData] = useState<ProjectPluginsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  // A toggle can succeed on settings.json and still change nothing in sessions:
  // .claude/settings.local.json outranks it on a key conflict. The PUT response
  // says so; hiding it would render the toggle as silently broken.
  const [warning, setWarning] = useState<string | null>(null);
  // The row whose config modal is open — manual 'configure'/'edit' click, or
  // an automatic open right after enabling a pack that needs it (see toggle).
  const [configFor, setConfigFor] = useState<ProjectPluginRow | null>(null);

  // Unmount guard shared by the initial load, the retry button, and the
  // reload-after-toggle: a ref (not a closure `let`) because `load` outlives
  // any single effect run. Flipped false in the effect cleanup below.
  const aliveRef = useRef(true);

  // Returns the freshly-loaded response so toggle() can inspect the just-
  // enabled row's configStatus without a second fetch or a stale-closure read
  // of `data`.
  const load = useCallback((): Promise<ProjectPluginsResponse | null> => {
    return fetchProjectPlugins(projectId)
      .then((d) => {
        if (!aliveRef.current) return null;
        setData(d);
        setError(null);
        return d;
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return null;
        setError(e instanceof Error ? e.message : String(e));
        return null;
      });
  }, [projectId]);

  useEffect(() => {
    aliveRef.current = true;
    void load();
    return () => {
      aliveRef.current = false;
    };
  }, [load]);

  const toggle = (row: ProjectPluginRow): void => {
    const wasEnabled = row.enabled;
    setBusy(row.name);
    setWarning(null);
    toggleProjectPlugin(projectId, row.name, !row.enabled)
      .then((res) => {
        if (!aliveRef.current) return null;
        setError(null);
        setWarning(res.warning ?? null);
        return load();
      })
      .then((fresh) => {
        // Close the loop without making the operator notice the amber chip
        // first: enabling a pack that needs config opens the modal right
        // away. Only on enable (not disable), and only when the reload
        // actually landed.
        if (!aliveRef.current || wasEnabled || fresh === null) return;
        const freshRow = fresh.plugins.find((p) => p.name === row.name);
        if (freshRow?.configStatus !== 'needs-config') return;
        // Never displace a modal the operator already has open: a reload
        // landing while they are typing into another pack's form must not
        // swap the dialog out from under them. Functional update so the
        // decision reads live state, not this closure's snapshot.
        setConfigFor((current) => (current === null ? freshRow : current));
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return;
        setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (aliveRef.current) setBusy(null);
      });
  };

  const overlaySources = data?.overlaySources?.join(', ') ?? '';
  const marketplaceVersion = data?.marketplaceVersion ?? '';

  return (
    <>
      <SectionTitle>
        <Trans>plugins</Trans>
      </SectionTitle>
      {error !== null && (
        <div className="mb-2">
          <ErrorBox message={error} onRetry={load} />
        </div>
      )}
      {warning !== null && (
        <div className="mb-2 rounded-xl border border-amber/40 bg-amber/10 px-3.5 py-2 font-mono text-[10.5px] text-amber">
          {warning}
        </div>
      )}
      {data === null && error === null ? (
        <Loading label={t`plugins…`} />
      ) : data !== null ? (
        <Card>
          {data.plugins.length === 0 ? (
            <div className="rounded-xl border border-dashed border-line px-3.5 py-4 font-mono text-[11.5px] text-ink-dim">
              <Trans>no plugins in the marketplace clone</Trans>
            </div>
          ) : (
            <div className="divide-y divide-line-soft">
              {data.plugins.map((row) => (
                <div key={row.name} className="flex flex-col gap-0.5 py-1.5 first:pt-0 last:pb-0">
                  <div className="flex items-center gap-3">
                    <span className="font-mono text-[11px] whitespace-nowrap text-ink-2">
                      {row.name}
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-ink-faint">
                      {row.description}
                    </span>
                    <StatusChip row={row} />
                    {row.status === 'missing' || row.status === 'behind' ? (
                      <RepairButton
                        projectId={projectId}
                        row={row}
                        marketplace={data.marketplaceName}
                        disabled={!data.canWrite}
                        onDone={load}
                      />
                    ) : null}
                    <ConfigureButton
                      row={row}
                      disabled={!data.canWrite}
                      onOpen={() => setConfigFor(row)}
                    />
                    <ToggleButton
                      row={row}
                      disabled={!data.canWrite}
                      busy={busy === row.name}
                      onToggle={() => toggle(row)}
                    />
                  </div>
                  {/* The detail is a second line, not only a tooltip: the reader
                      needs to see WHICH project the plugin was installed for. */}
                  {row.detail !== undefined && row.detail !== '' ? (
                    <div className="pl-1 font-mono text-[10px] text-ink-faint">{row.detail}</div>
                  ) : null}
                </div>
              ))}
            </div>
          )}
          <div className="mt-2 font-mono text-[10px] text-ink-faint">
            <Trans>
              marketplace v{marketplaceVersion} · repairs and toggles take effect in the next Claude Code
              session
            </Trans>
            {overlaySources !== '' ? t` · enabled state also reads: ${overlaySources}` : null}
          </div>
        </Card>
      ) : null}
      {configFor !== null && (
        // key on the row name: switching the modal from one pack to another
        // must REMOUNT it, not reuse the mounted instance. Without this the
        // form state (a half-typed block) would survive the swap and could be
        // saved under the other pack's key.
        <PluginConfigModal
          key={configFor.name}
          projectId={projectId}
          row={configFor}
          onClose={() => setConfigFor(null)}
          onSaved={() => {
            setConfigFor(null);
            void load();
          }}
        />
      )}
    </>
  );
}
