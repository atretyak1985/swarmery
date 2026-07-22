// Plugins section (/projects/:id, managed projects): the swarmery marketplace
// catalog with per-project enable/disable toggles. Writes edit the project's
// .claude/settings.json via the fenced PUT endpoint and take effect in the
// NEXT Claude Code session; core is locked (attach/detach owns its lifecycle).

import { useCallback, useEffect, useState } from 'react';
import type { ProjectPluginRow, ProjectPluginsResponse } from '../api/types';
import { fetchProjectPlugins, toggleProjectPlugin } from '../api';
import { Card, ErrorBox, Loading, SectionTitle } from './ui';

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
  if (row.locked) {
    return (
      <span
        className="font-mono text-[10px] text-ink-faint"
        title="core is managed via attach/detach"
      >
        via attach/detach
      </span>
    );
  }
  return (
    <button
      type="button"
      disabled={disabled || busy}
      onClick={onToggle}
      title={disabled ? 'read-only — daemon started without SWARMERY_ONBOARD_ROOTS' : undefined}
      className={`rounded-full border px-2.5 py-0.5 font-mono text-[10px] transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${
        row.enabled
          ? 'border-brand/40 bg-brand/10 text-brand hover:bg-brand/20'
          : 'border-line text-ink-faint hover:text-ink'
      }`}
    >
      {busy ? '…' : row.enabled ? 'on' : 'off'}
    </button>
  );
}

export function ProjectPlugins({ projectId }: { projectId: number }): JSX.Element {
  const [data, setData] = useState<ProjectPluginsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback((): void => {
    fetchProjectPlugins(projectId)
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

  const toggle = (row: ProjectPluginRow): void => {
    setBusy(row.name);
    toggleProjectPlugin(projectId, row.name, !row.enabled)
      .then(() => {
        setError(null);
        load();
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(null));
  };

  return (
    <>
      <SectionTitle>plugins</SectionTitle>
      {error !== null && (
        <div className="mb-2">
          <ErrorBox message={error} onRetry={load} />
        </div>
      )}
      {data === null && error === null ? (
        <Loading label="plugins…" />
      ) : data !== null ? (
        <Card>
          <div className="divide-y divide-line-soft">
            {data.plugins.map((row) => (
              <div key={row.name} className="flex items-center gap-3 py-1.5 first:pt-0 last:pb-0">
                <span className="font-mono text-[11px] whitespace-nowrap text-ink-2">
                  {row.name}
                </span>
                <span className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-ink-faint">
                  {row.description}
                </span>
                <ToggleButton
                  row={row}
                  disabled={!data.canWrite}
                  busy={busy === row.name}
                  onToggle={() => toggle(row)}
                />
              </div>
            ))}
          </div>
          <div className="mt-2 font-mono text-[10px] text-ink-faint">
            marketplace v{data.marketplaceVersion} · changes take effect in the next Claude Code
            session
          </div>
        </Card>
      ) : null}
    </>
  );
}
