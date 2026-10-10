import { defineConfig } from '@lingui/conf';
import { CATALOGS, type CatalogName, LOCALES, SOURCE_LOCALE } from './src/i18n/catalogs';

// Which source files each catalog owns — the Legs table of the extraction
// phase, one leg per catalog (plan D2). The sets are disjoint: a message is
// extracted into exactly one catalog, so legs never edit the same .po file.
const P = '<rootDir>/src/pages';
const INCLUDE: Record<CatalogName, string[]> = {
  'today-sessions': [
    `${P}/today`,
    `${P}/Overview.tsx`,
    `${P}/Sessions.tsx`,
    `${P}/SessionDetail.tsx`,
    `${P}/detail`,
    `${P}/NeedsYou.tsx`,
    `${P}/Approvals.tsx`,
    `${P}/Projects.tsx`,
    `${P}/ProjectDetail.tsx`,
    `${P}/ProjectOverview.tsx`,
    `${P}/ProjectSettings.tsx`,
  ],
  plans: [
    `${P}/plans`,
    `${P}/planning`,
    `${P}/Plans.tsx`,
    `${P}/PlanningMode.tsx`,
    `${P}/Playbooks.tsx`,
    `${P}/Routines.tsx`,
    `${P}/Board.tsx`,
  ],
  'insights-system': [
    `${P}/inbox`,
    `${P}/learning`,
    `${P}/health`,
    `${P}/Lessons.tsx`,
    `${P}/LessonsCalibration.tsx`,
    `${P}/Retro.tsx`,
    `${P}/Analytics.tsx`,
    `${P}/RoutingReport.tsx`,
    `${P}/system`,
    `${P}/system-hub`,
    `${P}/SystemHub.tsx`,
    `${P}/SystemShell.tsx`,
    `${P}/agent-hub`,
    `${P}/AgentHub.tsx`,
    `${P}/Memory.tsx`,
    `${P}/memory`,
    `${P}/knowledge`,
    `${P}/Architecture.tsx`,
    `${P}/Graphify.tsx`,
    `${P}/Serena.tsx`,
    `${P}/Docs.tsx`,
  ],
  shared: [
    '<rootDir>/src/components',
    '<rootDir>/src/workspace',
    '<rootDir>/src/App.tsx',
    `${P}/Settings.tsx`,
    `${P}/settings`,
    '<rootDir>/src/theme',
    '<rootDir>/src/lib',
    // App-level files outside every page folder: the route error boundary,
    // the API client's user-facing errors, the docs rail model.
    '<rootDir>/src/main.tsx',
    '<rootDir>/src/api.ts',
    '<rootDir>/src/api',
    `${P}/docsRail.ts`,
  ],
};

export default defineConfig({
  locales: [...LOCALES],
  sourceLocale: SOURCE_LOCALE,
  catalogs: CATALOGS.map((name) => ({
    path: `<rootDir>/src/locales/{locale}/${name}`,
    include: INCLUDE[name],
    // lib/glossary.ts is tied to docs/concepts.md by the Go drift test (D8).
    exclude: ['**/*.test.*', '<rootDir>/src/lib/glossary.ts'],
  })),
  format: 'po',
  // No line numbers in origins: a code edit above a message would otherwise
  // rewrite every .po it lands in, and parallel legs would conflict on them.
  formatOptions: { lineNumbers: false },
  compileNamespace: 'es',
});
