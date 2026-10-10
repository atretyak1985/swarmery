// The header's live usage chip — `◔ 28%` — and the owner of the Usage modal's
// open state. Both shells (fleet App + project WorkspaceShell) render just
// `<UsageChip />`; the chip carries its own `relative` wrapper because that is
// the positioning context the ≥768px popover variant of UsageModal anchors to.
//
// Data comes from the shared poller (lib/usageData.tsx), never from a fetch of
// its own — the chip and the modal must not be two pollers.
//
// Chrome is byte-identical to the plain button it replaces (and to ThemeToggle's
// neighbouring style) so swapping the label for a percentage cannot shift the
// header's height or horizontal rhythm.

import type { I18n } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { UsageAccount, UsageProvider, UsageWindow } from '../../api/types';
import { useActiveUsageAccount } from '../../lib/activeAccount';
import { useAccountReadiness } from '../../lib/accountReadiness';
import { useUsage } from '../../lib/usageData';
import { fmtResetsIn } from './format';
import { UsageModal } from './UsageModal';

const NO_AUTH_TIP = msg`Claude usage unavailable — run \`claude\` to log in`;

/**
 * Tip for a payload with nothing but not-connected providers. The daemon's own
 * hint headline is preferred when there is one — "Claude login expired" is the
 * fact the operator needs, and it differs per cause (expired, rejected, missing
 * scope, switched off) where the generic line does not.
 */
function noAuthTip(providers: readonly UsageProvider[], i18n: I18n): string {
  const hint = providers.find((p) => p.hint !== undefined)?.hint;
  if (hint === undefined) return i18n._(NO_AUTH_TIP);
  const command = hint.command;
  const fix =
    command !== undefined && command !== ''
      ? i18n._(msg`run \`${command}\``)
      : i18n._(msg`open usage`);
  return `${hint.title} — ${fix}`;
}

/**
 * The window the chip speaks for: the session window of the first healthy
 * provider, falling back to that provider's first window (a payload with only a
 * weekly window is still worth showing), then to nothing.
 */
function pickWindow(providers: readonly UsageProvider[]): UsageWindow | null {
  const healthy = providers.find((p) => p.status === 'ok');
  if (healthy === undefined) return null;
  return healthy.windows.find((w) => w.key.startsWith('session')) ?? healthy.windows[0] ?? null;
}

/**
 * Colour keyed to ACTUAL burn, with pace as an independent trigger.
 *
 * `ahead` means burning FASTER than the window elapses — over pace, the warning
 * state — so it is red even at a low percentage. Read UsageWindowRow's pace note
 * before touching this: the vocabulary is not invertible.
 */
function toneFor(w: UsageWindow): string {
  if (w.percentUsed > 90 || w.pace?.status === 'ahead') return 'text-red';
  if (w.percentUsed > 70) return 'text-amber';
  return 'text-ink-2';
}

/** One account's slice of the chip: `in 41%`, coloured by its own burn. */
interface ChipSegment {
  /**
   * Muted account prefix before the percentage. Empty for the default account
   * (and therefore for every single-account machine), so the multi-account chip
   * reads `◔ 2% · in 41%` and the single-account chip stays exactly `◔ 2%`.
   */
  label: string;
  text: string;
  tone: string;
}

interface ChipView {
  /** Segments after the glyph — one per reporting account, else one `usage`. */
  segments: ChipSegment[];
  tip: string;
}

function single(text: string, tone: string, tip: string): ChipView {
  return { segments: [{ label: '', text, tone }], tip };
}

/** The muted prefix naming a non-default account's segment. */
function segmentLabel(account: string): string {
  return account === 'default' ? '' : account.slice(0, 2);
}

function buildView(
  providers: readonly UsageProvider[],
  accounts: readonly UsageAccount[],
  error: string | null,
  lastUpdated: number | null,
  notReady: string | null,
  i18n: I18n,
): ChipView {
  const usageLabel = i18n._(msg`usage`);
  // Nothing has ever loaded and the fetch failed — the only genuinely blind
  // state, so the chip greys out. A failure AFTER a success keeps the last known
  // percentages instead (stale but informative) and says so in the tip, matching
  // how the modal keeps stale cards behind a "refresh failed" footer.
  if (lastUpdated === null && error !== null) {
    return single(usageLabel, 'text-ink-dim', i18n._(msg`Claude usage unavailable — ${error}`));
  }
  if (lastUpdated === null) {
    return single(usageLabel, 'text-ink-2', i18n._(msg`Subscription usage — loading…`));
  }

  // One segment per account that has a healthy window, in the daemon's account
  // order. An account with nothing to report (not connected, erroring) gets no
  // segment — a chip slot it cannot fill would be noise — but is still named in
  // the tip, so a disconnected second account remains discoverable without
  // opening the modal. Guard for a payload with no accounts[] at all (mock or
  // stub data): the top-level providers stand in as the single default account.
  const rows: readonly UsageAccount[] =
    accounts.length > 0 ? accounts : [{ account: 'default', providers: [...providers] }];
  const segments: ChipSegment[] = [];
  const tipParts: string[] = [];
  for (const row of rows) {
    const w = pickWindow(row.providers);
    if (w === null) {
      if (row.providers.some((p) => p.status === 'no-auth')) {
        const account = row.account;
        tipParts.push(i18n._(msg`${account} — not connected`));
      }
      continue;
    }
    const reset =
      w.resetText ?? (w.resetAt !== undefined ? fmtResetsIn(w.resetAt, Date.now()) : '');
    const pct = String(Math.round(w.percentUsed));
    const windowLabel = w.label;
    const detail = [i18n._(msg`${windowLabel} ${pct}% used`)];
    if (reset !== '') detail.push(reset);
    if (w.pace !== undefined) detail.push(w.pace.message);
    tipParts.push(`${row.account} — ${detail.join(', ')}`);
    // The scoped project's effective account failing the CLI-readiness probe
    // must not look healthy just because its quota reads fine — the segment
    // takes the warning tone (the whole chip, on a single-account machine).
    // `notReady` is null whenever readiness is unknown or unasked, so the
    // healthy single-account chip stays pixel-identical.
    const tone = row.account === notReady ? 'text-amber' : toneFor(w);
    segments.push({ label: segmentLabel(row.account), text: `${pct}%`, tone });
  }

  // A not-ready scoped account carries the warning tone even without a healthy
  // window to hang it on: the whole chip when it is the only story to tell,
  // else its own amber `!` slot alongside the healthy accounts' percentages.
  if (segments.length === 0) {
    // No healthy card anywhere. When EVERY card is a "not connected" one, say
    // what to connect — the daemon's own headline, which differs per cause
    // (expired, rejected, missing scope, switched off). A mix of not-connected
    // and genuinely broken cards has no single honest headline, so it stays
    // generic and sends the operator to the modal.
    const tone = notReady !== null ? 'text-amber' : 'text-ink-2';
    const all = rows.flatMap((a) => a.providers);
    if (all.length > 0 && all.every((p) => p.status === 'no-auth')) {
      return single(usageLabel, tone, noAuthTip(all, i18n));
    }
    return single(usageLabel, tone, i18n._(msg`Subscription usage`));
  }
  if (notReady !== null && !segments.some((s) => s.label === segmentLabel(notReady))) {
    segments.push({ label: segmentLabel(notReady), text: '!', tone: 'text-amber' });
  }

  if (error !== null) tipParts.push(i18n._(msg`refresh failed`));
  return { segments, tip: tipParts.join(' · ') };
}

export function UsageChip(): JSX.Element {
  const { t, i18n } = useLingui();
  const { accounts, providers, error, lastUpdated, setModalOpen } = useUsage();
  const [open, setOpen] = useState(false);
  // The scoped project's effective account, threaded into the modal so it can
  // name and lift the active account. Fetched here, not in the modal — the
  // modal stays pure presentation (see its header note).
  const active = useActiveUsageAccount(open);
  // Readiness of the scoped effective account (shared cache with the banner —
  // not a second fetcher). Only an ANSWERED "no" marks the chip: null (never
  // probed / OAuth off / list unavailable) keeps the chip exactly as it was.
  const readiness = useAccountReadiness();
  const notReady =
    active !== null &&
    readiness?.find((a) => a.key === active.account)?.runnable === false
      ? active.account
      : null;

  // Registering the open modal is what bumps the SHARED cadence 120s → 30s; it
  // never starts a second poller. Reference-counted in the provider, so
  // StrictMode's mount/unmount/mount cycle nets out correctly.
  useEffect(() => {
    if (!open) return undefined;
    setModalOpen(true);
    return () => setModalOpen(false);
  }, [open, setModalOpen]);

  const view = buildView(providers, accounts, error, lastUpdated, notReady, i18n);

  return (
    <span className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={t`Subscription usage`}
        // `data-tip`, never `title`: native tooltips were removed app-wide in
        // favour of the themed TooltipLayer (components/Tooltip.tsx), which also
        // wires aria-describedby — the accessible NAME still comes from
        // aria-label above.
        data-tip={view.tip}
        // A single segment tints the whole button (glyph included) exactly as
        // the pre-multi-account chip did; with several, each segment carries its
        // own tone and the chrome stays neutral so no one account's colour
        // claims the glyph.
        className={`rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[11px] font-semibold tabular-nums transition-colors hover:bg-surface2 ${view.segments.length === 1 ? (view.segments[0]?.tone ?? 'text-ink-2') : 'text-ink-2'}`}
      >
        <span aria-hidden="true">◔</span>{' '}
        {view.segments.map((seg, i) => (
          <span key={`${seg.label}:${String(i)}`}>
            {i > 0 && <span className="text-ink-dim"> · </span>}
            {seg.label !== '' && <span className="font-normal text-ink-dim">{seg.label} </span>}
            <span className={view.segments.length > 1 ? seg.tone : undefined}>{seg.text}</span>
          </span>
        ))}
      </button>
      <UsageModal open={open} onClose={() => setOpen(false)} active={active} />
    </span>
  );
}
