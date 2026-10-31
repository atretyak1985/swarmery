// Global settings (/settings — session mode, handoff §3, §9): the frame every place
// shares, one accordion group of rows. `?tab=` keeps its four old values, so the
// retired /projects list still lands on Projects, and adds the rows that the old
// Appearance tab held in one column.
//
//   Appearance    — theme, compact rows, motion: the toggles really switch them.
//   Palette       — ThemePickerPanel, the swatches with their live preview.
//   Language      — the UI locale.
//   Accounts      — the Claude accounts on this host (AccountsSection).
//   Notifications — the shared NotifyPrefsContext, the state the mounted
//                    useBrowserNotifications hook reads, so a switch here drives
//                    the live background toasts.
//   Projects      — the project list (<Projects embedded />).
//   This machine  — daemon health, the auto-approve note, worktrees, connectors.
//   Keyboard      — the global keys.
//
// Project settings stay at /p/:slug/settings (pages/ProjectSettings.tsx).

import { useLingui } from '@lingui/react/macro';
import { useState, type ReactNode } from 'react';
import { AccountsSection } from '../components/AccountsSection';
import { ConnectorsSection } from '../components/ConnectorsSection';
import { ExplainPair } from '../components/Explain';
import { LanguagePicker } from '../components/LanguagePicker';
import { PageDetail, PagePlace, type PageToggle, usePlaceParams } from '../components/place/PageDetail';
import { useHealth, versionLabel } from '../lib/health';
import {
  type NotifyPrefs,
  notificationsSupported,
  permissionState,
  requestPermission,
  savePrefs,
} from '../lib/notifications';
import { useNotifyPrefs } from '../lib/notifyPrefsContext';
import { useTheme } from '../lib/theme';
import type { Mode } from '../theme/palettes';
import { ThemePickerPanel } from '../theme/ThemePicker';
import { Projects } from './Projects';
import { WorktreesPanel } from './settings/WorktreesPanel';

const SETTINGS_TABS = [
  'appearance',
  'palette',
  'language',
  'accounts',
  'notifications',
  'projects',
  'machine',
  'keyboard',
] as const;

type SettingsTab = (typeof SETTINGS_TABS)[number];

/** A section of an existing panel under the detail's blocks. */
function Section({ title, children }: { title?: ReactNode; children: ReactNode }): JSX.Element {
  return (
    <section className="mt-[18px]">
      {title !== undefined && (
        <div className="mb-2 font-mono text-[10px] leading-[normal] tracking-[0.12em] text-ink-faint uppercase">
          {title}
        </div>
      )}
      {children}
    </section>
  );
}

function AppearanceDetail(): JSX.Element {
  const { t } = useLingui();
  const { mode, setMode, compact, setCompact, motion, setMotion } = useTheme();
  const toggles: PageToggle[] = [
    {
      key: 'theme',
      label: t`Theme`,
      hint: t`Also toggled with t`,
      options: [
        { value: 'dark', label: t`dark` },
        { value: 'light', label: t`light` },
        { value: 'system', label: t`system` },
      ],
      value: mode,
      onPick: (next) => setMode(next as Mode),
    },
    { key: 'compact', label: t`Compact rows`, hint: t`Tighter rows in every list`, on: compact, onChange: setCompact },
    { key: 'motion', label: t`Motion`, hint: t`Spinning and pulsing state glyphs`, on: motion, onChange: setMotion },
  ];
  return <PageDetail kind={t`settings`} meta={t`appearance`} title={t`Appearance`} toggles={toggles} />;
}

function NotificationsDetail(): JSX.Element {
  const { t } = useLingui();
  const { prefs, setPrefs } = useNotifyPrefs();
  const [denied, setDenied] = useState(permissionState() === 'denied');
  const supported = notificationsSupported();

  const flip = (key: keyof NotifyPrefs): void => {
    void (async () => {
      const enabling = !prefs[key];
      // A browser asks for the permission on the first switch on; the click is
      // the user gesture it requires.
      if (enabling && !(await requestPermission())) {
        setDenied(permissionState() === 'denied');
        return;
      }
      const next = { ...prefs, [key]: enabling };
      savePrefs(next);
      setPrefs(next);
    })();
  };

  const row = (key: keyof NotifyPrefs, label: string, hint: string): PageToggle => ({
    key,
    label,
    hint,
    on: prefs[key],
    disabled: !supported,
    onChange: () => flip(key),
  });

  return (
    <PageDetail
      kind={t`settings`}
      meta={t`notifications`}
      title={t`Notifications`}
      lead={
        !supported
          ? t`this browser does not support the Notifications API`
          : denied
            ? t`notifications are blocked — allow them in the browser site settings, then re-toggle`
            : null
      }
      toggles={[
        row('approvalRequested', t`Needs you`, t`A request needs a decision`),
        row('sessionFinished', t`Finished`, t`An active session ended`),
        row('sessionError', t`Failed`, t`An error event appeared`),
      ]}
      paras={[
        t`fires only while this tab is in the background; click a notification to jump to the approval or session`,
      ]}
    />
  );
}

function MachineDetail(): JSX.Element {
  const { t } = useLingui();
  const { health, unreachable } = useHealth();
  return (
    <PageDetail
      kind={t`settings`}
      meta={t`this machine`}
      title={t`This machine`}
      lead={t`What runs here for every project: the daemon, its worktree janitor and the MCP servers Claude Code has configured.`}
      kv={[
        {
          k: t`daemon`,
          v: unreachable ? t`daemon unreachable` : t`daemon healthy`,
          tone: unreachable ? 'red' : 'green',
        },
        ...(health !== null ? [{ k: t`version`, v: `${versionLabel(health)} · :7777` }] : []),
      ]}
      // A preset is scoped to one project (PermissionPresets takes a projectId).
      paras={[t`auto-approve presets are configured per project — open a project's Settings to set them`]}
    >
      {/* The janitor sweeps every project on this machine without being asked, so
          this is the account of what it did. */}
      <Section title={t`worktrees`}>
        <WorktreesPanel />
      </Section>
      {/* `claude mcp list` reports user/claudeai/plugin scopes: they belong to the
          machine, not to any one project. */}
      <Section title={t`connectors`}>
        <ConnectorsSection />
      </Section>
    </PageDetail>
  );
}

function SettingsDetail({ tab }: { tab: SettingsTab }): JSX.Element {
  const { t } = useLingui();
  switch (tab) {
    case 'appearance':
      return <AppearanceDetail />;
    case 'palette':
      return (
        <PageDetail
          kind={t`settings`}
          meta={t`palette`}
          title={t`Palette`}
          lead={t`Hover a palette to preview it; a click keeps it.`}
        >
          <Section>
            <ThemePickerPanel />
          </Section>
        </PageDetail>
      );
    case 'language':
      return (
        <PageDetail
          kind={t`settings`}
          meta={t`language`}
          title={t`Language`}
          lead={t`translates the dashboard itself; text written by agents (plans, reports, lessons, advisor recommendations) and messages from the daemon stay in English`}
        >
          <Section>
            <LanguagePicker />
          </Section>
        </PageDetail>
      );
    case 'accounts':
      return (
        <PageDetail
          kind={t`settings`}
          meta={t`accounts`}
          title={t`Accounts`}
          lead={t`Sessions pick the default account; when it hits its window the daemon pauses it and queues sessions until the reset.`}
        >
          <Section title={<ExplainPair id="claude-account">{t`claude accounts`}</ExplainPair>}>
            <AccountsSection />
          </Section>
        </PageDetail>
      );
    case 'notifications':
      return <NotificationsDetail />;
    case 'projects':
      return (
        <PageDetail kind={t`settings`} meta={t`projects`} title={t`Projects`}>
          <Projects embedded />
        </PageDetail>
      );
    case 'machine':
      return <MachineDetail />;
    case 'keyboard':
      return (
        <PageDetail
          kind={t`settings`}
          meta={t`keyboard`}
          title={t`Keyboard`}
          kv={[
            { k: 'j / k', v: t`move selection` },
            { k: 'e', v: t`primary action` },
            { k: 'x', v: t`secondary action` },
            { k: '1 – 5', v: t`Today · Inbox · Needs you · Sessions · Plans` },
            { k: 't', v: t`toggle theme` },
            { k: '⌘K', v: t`search & actions` },
            { k: 'Esc', v: t`close` },
          ]}
        />
      );
  }
}

export function Settings(): JSX.Element {
  const { t } = useLingui();
  const { tab, pick } = usePlaceParams<SettingsTab>(SETTINGS_TABS, 'appearance');
  const row = (key: SettingsTab, title: string, summary: string): { key: string; glyph: string; title: string; summary: string } => ({
    key,
    glyph: '⚙',
    title,
    summary,
  });
  return (
    <PagePlace
      title={t`Settings`}
      searchHint={t`filter settings`}
      groups={[
        {
          key: 'settings',
          label: t`Settings`,
          items: [
            row('appearance', t`Appearance`, t`theme · density · motion`),
            row('palette', t`Palette`, t`six palettes with a live preview`),
            row('language', t`Language`, t`English · Українська`),
            row('accounts', t`Accounts`, t`Claude accounts on this host`),
            row('notifications', t`Notifications`, t`needs-you · finished · failed`),
            row('projects', t`Projects`, t`attach · archive · plugins`),
            row('machine', t`This machine`, t`daemon · worktrees · connectors`),
            row('keyboard', t`Keyboard`, t`j/k · e/x · 1–5 · t · ⌘K`),
          ],
        },
      ]}
      openGroup="settings"
      selected={tab}
      onOpenGroup={() => undefined}
      onSelect={(key) => pick(key as SettingsTab)}
      detail={<SettingsDetail key={tab} tab={tab} />}
    />
  );
}
