// Templates tab (System screen, read-only): overlays/<dir>/ entries with the
// safe project.json subset the API serves (never secrets/env). A project.json
// that exists but fails to parse keeps its row with a red "parse error" mark.

import { useEffect, useState } from 'react';
import type { SystemOverlay, SystemOverlays } from '../../api/types';
import { fetchSystemOverlays } from '../../api/system';
import { Empty, ErrorBox, Loading } from '../../components/ui';

function Field({ label, value }: { label: string; value: string | null }): JSX.Element | null {
  if (value === null || value === '') return null;
  return (
    <span className="mr-4 whitespace-nowrap">
      {label} <b className="font-medium text-ink-2">{value}</b>
    </span>
  );
}

function ChipList({ label, values }: { label: string; values: string[] }): JSX.Element | null {
  if (values.length === 0) return null;
  return (
    <span className="flex flex-wrap items-center gap-1">
      <span className="text-ink-dim">{label}</span>
      {values.map((v) => (
        <span
          key={v}
          className="rounded-full border border-line px-2 py-px font-mono text-[10px] whitespace-nowrap text-ink-dim"
        >
          {v}
        </span>
      ))}
    </span>
  );
}

function OverlayRow({ overlay }: { overlay: SystemOverlay }): JSX.Element {
  return (
    <div className="border-b border-line-soft px-3.5 py-2.5 last:border-b-0">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[13px] font-semibold text-ink">{overlay.dir}/</span>
        {overlay.displayName !== null && (
          <span className="text-[12.5px] text-ink-2">{overlay.displayName}</span>
        )}
        {overlay.parseError && (
          <span
            className="rounded-full border border-red/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-red"
            title="project.json exists but is not valid JSON — fields unavailable"
          >
            parse error
          </span>
        )}
      </div>
      {!overlay.parseError && (
        <div className="mt-1.5 flex flex-wrap items-center gap-y-1 font-mono text-[10.5px] text-ink-dim">
          <Field label="name" value={overlay.name} />
          <Field label="mainApp" value={overlay.mainApp} />
          <Field label="codePath" value={overlay.codePath} />
        </div>
      )}
      {!overlay.parseError && (
        <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10.5px]">
          <ChipList label="repos" values={overlay.repos} />
          <ChipList label="packs" values={overlay.enabledPacks} />
        </div>
      )}
      <div className="mt-1 font-mono text-[10.5px] break-all text-ink-dim/70">{overlay.path}</div>
    </div>
  );
}

export function TemplatesTab({ refreshKey }: { refreshKey: number }): JSX.Element {
  const [data, setData] = useState<SystemOverlays | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    fetchSystemOverlays()
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setError(null);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [refreshKey, attempt]);

  if (error !== null) return <ErrorBox message={error} onRetry={() => setAttempt((a) => a + 1)} />;
  if (data === null) return <Loading label="overlays…" />;

  return (
    <div className="mt-3">
      {!data.schemaPresent && (
        <div className="mb-2.5 rounded-lg border border-amber/45 bg-amber/5 px-3.5 py-2 font-mono text-[11px] text-amber">
          overlays/_schema/project.schema.json is missing — overlay validation unavailable
        </div>
      )}
      {data.overlays.length === 0 ? (
        <Empty>
          no overlays found — the daemon's working copy has no overlays/ directory, or it is empty
          on this machine
        </Empty>
      ) : (
        <div className="overflow-hidden rounded-xl border border-line bg-surface">
          {data.overlays.map((overlay) => (
            <OverlayRow key={overlay.dir} overlay={overlay} />
          ))}
        </div>
      )}
    </div>
  );
}
