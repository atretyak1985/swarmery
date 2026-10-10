// Global settings (/settings — session mode), Canvas v3 phase 8: one place with
// four tabs on `?tab=` (Appearance · Accounts · Notifications · Projects), so the
// retired /projects list redirects onto ?tab=projects.
//
//   Appearance    — ThemePickerPanel (mode segments + palette list; it renders
//                    its own "appearance" eyebrow), the UI language picker,
//                    then the host footer: the auto-approve note, daemon
//                    health, worktrees, connectors.
//   Accounts      — the Claude accounts on this host (AccountsSection).
//   Notifications — NotifySettings, wired to the shared NotifyPrefsContext (the
//                    state the mounted useBrowserNotifications hook in AppShell
//                    reads, so toggles here drive live background toasts).
//   Projects      — the project list (<Projects embedded />).
//
// Project settings stay at /p/:slug/settings (pages/ProjectSettings.tsx).

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { AccountsSection } from '../components/AccountsSection';
import { ConnectorsSection } from '../components/ConnectorsSection';
import { ExplainPair } from '../components/Explain';
import { LanguagePicker } from '../components/LanguagePicker';
import { NotifySettings } from '../components/NotifySettings';
import { type TabItem, Tabs, useTabParam } from '../components/Tabs';
import { SectionTitle } from '../components/ui';
import { useHealth, versionLabel, versionTitle } from '../lib/health';
import { useNotifyPrefs } from '../lib/notifyPrefsContext';
import { ThemePickerPanel } from '../theme/ThemePicker';
import { Projects } from './Projects';
import { WorktreesPanel } from './settings/WorktreesPanel';

type SettingsTab = 'appearance' | 'accounts' | 'notifications' | 'projects';

const SETTINGS_TABS: readonly SettingsTab[] = ['appearance', 'accounts', 'notifications', 'projects'];

const TAB_ITEMS: readonly { id: SettingsTab; label: MessageDescriptor }[] = [
  { id: 'appearance', label: msg`Appearance` },
  { id: 'accounts', label: msg`Accounts` },
  { id: 'notifications', label: msg`Notifications` },
  { id: 'projects', label: msg`Projects` },
];

/** Appearance plus the host-level footer (auto-approve note, daemon, worktrees,
 * connectors) — everything the old single page showed besides accounts and
 * notifications. */
function AppearanceTab(): JSX.Element {
  const { health, unreachable } = useHealth();
  const daemonOk = !unreachable;
  return (
    <>
      <div className="mt-5">
        <ThemePickerPanel />
      </div>

      {/* Language — the UI locale (plan D5). Only the dashboard's own chrome is
          translated: text from agents and the daemon passes through verbatim
          (plan D7), and the note says so. */}
      <SectionTitle>
        <Trans>language</Trans>
      </SectionTitle>
      <div className="rounded-xl border border-line bg-surface p-3">
        <LanguagePicker />
        <p className="mt-2.5 text-[10.5px] leading-snug text-ink-faint">
          <Trans>
            translates the dashboard itself; text written by agents (plans, reports, lessons, advisor
            recommendations) and messages from the daemon stay in English
          </Trans>
        </p>
      </div>

      {/* Auto-approve — permission presets are dispatch/approval settings scoped
          to a single project (PermissionPresets needs a projectId), so they are
          configured per project, not globally. */}
      <SectionTitle>
        <Trans>auto-approve</Trans>
      </SectionTitle>
      <div className="rounded-xl border border-dashed border-line px-3.5 py-4 font-mono text-[11.5px] text-ink-dim">
        <Trans>auto-approve presets are configured per project — open a project's Settings to set them</Trans>
      </div>

      {/* Daemon — health + version from the shared poll. */}
      <SectionTitle>
        <Trans>daemon</Trans>
      </SectionTitle>
      <div className="flex items-center gap-2 rounded-xl border border-line bg-surface px-3.5 py-4 font-mono text-[11.5px] text-ink-dim">
        <span
          aria-hidden="true"
          className={`inline-block h-[7px] w-[7px] rounded-full ${daemonOk ? 'bg-green' : 'bg-red'}`}
        />
        {daemonOk ? <Trans>daemon healthy</Trans> : <Trans>daemon unreachable</Trans>}
        {health !== null && (
          <span title={versionTitle(health)}>
            {' '}
            · {versionLabel(health)} · :7777
          </span>
        )}
      </div>

      {/* Worktrees — the janitor's inventory + what it decided. Host-level: it
          sweeps every project on this machine without being asked, so this is
          the account of what it did. */}
      <SectionTitle>
        <Trans>worktrees</Trans>
      </SectionTitle>
      <WorktreesPanel />

      {/* Connectors — the MCP servers Claude Code has configured on THIS host
          (`claude mcp list` reports user/claudeai/plugin scopes, which belong to
          the machine, not to any one project). */}
      <SectionTitle>
        <Trans>connectors</Trans>
      </SectionTitle>
      <ConnectorsSection />
    </>
  );
}

function NotificationsTab(): JSX.Element {
  const { prefs, setPrefs } = useNotifyPrefs();
  return (
    <>
      <SectionTitle>
        <Trans>notifications</Trans>
      </SectionTitle>
      <NotifySettings prefs={prefs} onChange={setPrefs} />
    </>
  );
}

export function Settings(): JSX.Element {
  const { t, i18n } = useLingui();
  const [tab, setTab] = useTabParam<SettingsTab>('tab', SETTINGS_TABS, 'appearance');
  const tabs: TabItem<SettingsTab>[] = TAB_ITEMS.map((item) => ({ id: item.id, label: i18n._(item.label) }));
  const label = tabs.find((item) => item.id === tab)?.label ?? t`Appearance`;

  return (
    <div className="px-4 pt-5 pb-10 desk:px-8 desk:pt-7">
      <h1 className="mb-3 font-display text-[20px] font-medium tracking-[-0.01em] text-ink">
        <Trans>Settings</Trans>
      </h1>
      <Tabs tabs={tabs} value={tab} onChange={setTab} ariaLabel={t`Settings`} />
      <div role="tabpanel" aria-label={label}>
        {tab === 'appearance' && <AppearanceTab />}
        {tab === 'accounts' && (
          <>
            {/* The Claude accounts swarmery knows about on THIS host
                (multi-account, phase 7); AccountsSection self-fetches. */}
            <SectionTitle>
              <ExplainPair id="claude-account">
                <Trans>accounts</Trans>
              </ExplainPair>
            </SectionTitle>
            <AccountsSection />
          </>
        )}
        {tab === 'notifications' && <NotificationsTab />}
        {tab === 'projects' && <Projects embedded />}
      </div>
    </div>
  );
}
