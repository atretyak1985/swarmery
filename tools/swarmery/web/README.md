# swarmery web

The dashboard SPA of the `tools/swarmery` daemon: React 19 + Vite + Tailwind 4,
embedded into the Go binary with `go:embed`. Build, run and test commands are in
[`../README.md`](../README.md) and the repository's root `CLAUDE.md`
("Commands"); this file covers what a contributor to the web app needs on top.

| Command                                 | What it does                                                                                                                        |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `npm run dev`                           | Compiles the catalogs, then starts Vite (proxies `/api` to `:7777`). `VITE_MOCK=1 npm run dev` runs on fixture data with no daemon. |
| `npm run build`                         | Compiles the catalogs, `tsc --noEmit`, `vite build`.                                                                                |
| `npm test`                              | Compiles the catalogs, then Vitest (incl. the i18n gates below).                                                                    |
| `npm run lint`                          | Biome lint over `src/`. Biome is also the formatter: never run Prettier here.                                                       |
| `npm run i18n:extract`                  | Re-extracts every message from the source into `src/locales/{en,uk}/*.po`.                                                          |
| `npm run i18n:check`                    | Extract + `lingui compile --strict` (fails on an untranslated message).                                                             |
| `node scripts/i18n-literals.mjs --list` | Lists the user-visible literals that bypass Lingui.                                                                                 |
| `node scripts/i18n-layout.mjs`          | Ukrainian layout gate: 12 routes × 1024/1440 px in mock mode, fails on any overflow.                                                |
| `npm run screenshots`                   | Mock-mode screenshots + scroll invariants (`scripts/screenshot.mjs`).                                                               |

## Adding a user-visible string

The UI ships in English (the source locale) and Ukrainian, through
[Lingui](https://lingui.dev). Every string an operator can read goes through a
Lingui macro; the English text in the source code is the message id.

1. **Write it with a macro**, never as a bare literal:
   - JSX text: `<Trans>Run phase</Trans>` (`import { Trans } from '@lingui/react/macro'`);
   - an attribute or a string in a component: `const { t } = useLingui();` then
     ``aria-label={t`Close`}`` (`useLingui` from `@lingui/react/macro`);
   - a label kept in a module-level table (nav items, tab lists, options):
     `` msg`Inbox` `` (`import { msg } from '@lingui/core/macro'`), rendered with
     `i18n._(descriptor)` at render time so a locale switch shows up;
   - counts: `plural(n, { one: '# phase', other: '# phases' })` (from `@lingui/core/macro`) — Ukrainian
     needs its `one/few/many/other` forms, so never concatenate a number with
     an English noun.
   Dates and numbers go through `src/lib/format.ts`, which formats with the
     active locale (`currentLocale()` in `src/i18n/locale.ts`).
2. **Extract**: `npm run i18n:extract`. The new `msgid` lands in the catalog
   that owns the file — `lingui.config.ts` maps every source folder to one of
   the four catalogs (`today-sessions`, `plans`, `insights-system`, `shared`).
   A new page folder must be added to that map; `src/i18n/coverage.test.ts`
   fails for a macro in a file no catalog owns.
3. **Translate** the new entry's `msgstr` in `src/locales/uk/<catalog>.po`
   (the `en` catalog needs nothing — the id is the English text). Keep every
   `{placeholder}`, `#` and plural form of the source: `src/i18n/catalog.test.ts`
   fails on an empty `msgstr` or a lost placeholder. Place names (Today, Inbox,
   Needs you, Sessions, Plans, Health, Learning, Knowledge, Docs, System,
   Settings) stay English in both locales; they match the URLs and the docs.
4. **Compile** happens on its own: `npm run dev`, `npm run build` and
   `npm test` all run `lingui compile` first. The compiled `src/locales/*/*.mjs`
   files are gitignored; commit only the `.po` files.

### The literal budget

Biome has no `jsx-no-literals` rule, so `scripts/i18n-literals.mjs` walks the
source AST and counts user-visible strings that bypass Lingui (JSX text,
visible attributes such as `title`/`aria-label`/`placeholder`/`data-tip`, label
props, sentence-like strings in view models and API error fallbacks).
`src/i18n/literals.test.ts` holds that count to `I18N_LITERALS_BUDGET` in
`src/i18n/budget.ts`, which is **0** and only ever goes down. When `npm test`
fails on it, run

```bash
node scripts/i18n-literals.mjs --list                      # file:line  text, then the total
node scripts/i18n-literals.mjs --list --paths src/pages/plans   # only one area
```

and wrap each listed string in a macro. A string that is genuinely not UI copy
— a key name printed on a keyboard, an env var to paste into a shell, a demo
fixture — takes an `i18n-ignore` comment on its line or the line above, with
the reason: `{/* i18n-ignore — an env assignment to paste into a shell */}`.
Anything under `src/mock/`, `src/terminal/`, `src/i18n/`, `src/test/`, tests
and `src/lib/glossary.ts` (the explainer copy, tied to `docs/concepts.md` by the
Go drift test) is outside the scan.

### Gotchas

- `npm run i18n:check` **rewrites the `.po` files** (`lingui extract --clean`
  drops entries whose source is gone). Run it on a clean
  tree and commit or discard what it changes; it is not a read-only check.
- Text the daemon sends — API `error`/`hint` fields, lesson and advisor copy,
  phase reports, anything an agent wrote — is shown verbatim and stays English;
  only the dashboard's own chrome is translated.
- Ukrainian runs 20–40 % longer than English. After adding labels to a dense
  row (chips, buttons, table headers), run `node scripts/i18n-layout.mjs`
  (it starts a mock-mode dev server itself; `--out <dir>` saves screenshots,
  `--locale en` is the English control run). Fix an overflow with layout —
  `flex-wrap`, `min-w-0`, `truncate` plus a `title` — never by shortening the
  translation.
