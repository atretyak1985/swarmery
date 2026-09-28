# Usage — live subscription quotas in the header

The header's `◔` chip and the Usage modal behind it answer one question: **how much of my
Claude subscription quota is left, and am I burning it faster than the clock?** The numbers
are the *real* reading — the daemon asks Anthropic for the operator's own quota windows
using the operator's own `claude` login, the same call `claude /usage` makes. It is not
derived from indexed transcripts.

> [!IMPORTANT]
> Two rules hold everywhere in this surface: **no number is ever fabricated**, and **no
> failure is ever silent**. Every failure mode — opted out, not logged in, token rejected,
> rate limited, endpoint changed — degrades to a visible per-provider error card. A missing
> card would be a lie; a wrong number would be worse.

---

## 1. Data sources

### Which accounts appear

The account list is **not** discovered from credentials — it is the same list the ingest
pipeline reads transcripts from. Each configured projects root (`SWARMERY_PROJECTS_ROOTS`;
`auto` globs `~/.claude*/projects` once at daemon startup) is one account: the root's parent
directory is that account's config dir, and the account's name is derived from that
directory's name — `~/.claude` → `default`, `~/.claude-work` → `work`. This is the
same key session rows are stamped with, so a usage card and a session badge always agree on
what an account means.

Practical consequences: a new account shows up after its config dir has a `projects/`
directory (i.e. at least one session ran under it) **and** the daemon restarts — the `auto`
glob is evaluated once at boot. With no roots configured at all, the list is exactly one
`default` account, which is the pre-multi-account behaviour. The connect endpoints
(section 6) accept only accounts on this list — the API cannot be used to store a
credential under an arbitrary name.

### Credential resolution, per account

Every account first checks **swarmery's own store** — `~/.swarmery/credentials/<account>.json`,
written by the dashboard's *Connect account* flow (dir `0700`, file `0600`, atomic writes).
A store hit wins outright. That file is swarmery's alone: *Reconnect* replaces it through the
same atomic write, *Disconnect* removes it, and a card fed by it is marked
`connectedVia: "swarmery"` in the payload — which is how the modal knows which cards those two
actions belong on.

For a **non-default** account, connect additionally performs a **one-time credential
handoff**: the stored credential is copied once into `<configDir>/.credentials.json`
(mode `0600`, same `claudeAiOauth` shape) so the `claude` CLI can adopt it as that
account's login. The handoff is **write-once** — swarmery never refreshes that file, never
overwrites an existing one, and never touches the default account's `~/.claude`; token
rotation continues to happen only in swarmery's own store. Evidence and rationale:
`docs/claude-cli-credential-behaviour.md` (measured 2026-08-12).

After the store, resolution depends on the account:

- A **named account** (non-default) reads `<configDir>/.credentials.json` **exclusively** —
  no home-dir fallbacks, no `CLAUDE_CONFIG_DIR`, no keychain. Any fallback here would
  resolve the *default* account's credential and publish its quota under another account's
  name; a miss renders that account's `Connect` card instead.
- The **default account** falls through to the legacy chain. **First hit wins**, and every
  individual miss is silent (an unreadable or absent source is just the next candidate):

| # | Source | Notes |
|---|---|---|
| 1 | `$CLAUDE_CONFIG_DIR/.credentials.json` | only when `CLAUDE_CONFIG_DIR` is set; mirrors the CLI's own override |
| 2 | `~/.claude/.credentials.json` | the usual location |
| 3 | `~/.config/claude/.credentials.json` | alternate CLI layout |
| 4 | macOS Keychain item `Claude Code-credentials` | macOS only, via `security find-generic-password`; bounded to 5s so a keychain prompt cannot wedge a dashboard poll |

Both credential shapes the CLI has shipped are accepted: the credential nested under
`claudeAiOauth`, and a bare credential object at the file root. Anything without an
`accessToken` counts as *absent*, not as an error. Exhausting an account's sources yields
its `not connected` card, never a 500.

The quota itself comes from `GET https://api.anthropic.com/api/oauth/usage` (bearer +
`anthropic-beta: oauth-2025-04-20`); token refresh, when needed, goes to
`https://platform.claude.com/v1/oauth/token`. The credential must carry the `user:profile`
scope — but only a *populated* scope list missing it is rejected up front, because the CLI
does not always persist a scope list, and rejecting those would lock out genuinely
logged-in operators.

**Optional second card.** When `SWARMERY_USAGE_LIMITS` is set, a `Telemetry estimate`
provider is appended, computed from the daemon's own indexed token counts instead of from
Anthropic. It is emitted **only** when that variable is set — no variable, no card. It is
labelled explicitly so it can never be mistaken for the live reading:

```bash
SWARMERY_USAGE_LIMITS='{"session5h":{"label":"5-hour session","tokens":50000000,"windowHours":5},
                        "weekly":{"label":"Weekly","tokens":300000000,"windowHours":168}}'
```

`used` counts indexed input+output tokens in the rolling window across **all** projects,
archived included — quota is billed regardless of whether you archived the project. The
window is anchored to a fixed epoch grid so the "resets in" countdown is stable between
polls; it is an estimate of the reset schedule, not Anthropic's actual one. A malformed
value is the one configuration error that answers HTTP 500 rather than degrading.

## 2. What the modal shows

- **The chip** — `◔ 42%` for the session window of the first healthy provider (falling back
  to that provider's first window, so a payload with only a weekly window still says
  something). Red above 90% used **or** whenever pace is `ahead`; amber above 70%. The
  tooltip carries label, percentage, reset text and the pace message. `◔ usage` greyed out
  means nothing has ever loaded; `◔ usage` in normal tone means logged out.
- **One card per provider** — glyph, name, plan chip (`Max` / `Pro` / `Team`, derived from
  the credential's `subscriptionType`, else a `rateLimitTier` keyword match, never guessed),
  and a status badge: nothing for `ok`, `error`, or `not connected` for `no-auth`. A failed
  provider renders as that card with a red error block and no rows — it never takes the
  modal down.
- **One row per window** — label, percentage, a progress bar coloured by *actual* burn, a
  hairline marker showing how much of the window's **time** has elapsed, the inverse
  percentage, `resets in …`, an absolute reset chip, and the pace line.
- **Display mode** — `Used` / `Remaining` toggle. The bar's colour always tracks real usage:
  in `Remaining` mode a nearly-exhausted window still reads red, never "mostly empty,
  therefore green".
- **Connect / Reconnect / Disconnect** — a card with no readable credential offers
  `Connect account`: swarmery mints the PKCE authorization, you approve it in a browser and
  paste back the `code#state` the callback page shows. A card whose *swarmery-stored*
  credential has gone bad (refresh declined, token rejected, scope missing) offers the same
  flow as `Reconnect` — and deliberately shows **no** `claude` command, because the CLI does
  not write to swarmery's store and running it would change nothing. `disconnect`, in the card
  footer, appears only on cards marked `connectedVia: "swarmery"` and asks for an inline
  confirmation before it removes anything; see section 4 for exactly what it removes.
- **Per-window hide** — the `◉` toggle collapses a window you do not care about;
  `show hidden (N)` restores them. Both preferences persist in `localStorage` under
  `swarmery.usage.prefs`, keyed per provider **name** plus the server's window `key`, and
  are **global** — quota is an account-level fact, so a hidden window stays hidden across
  projects. Keys the daemon stops reporting are pruned automatically.
- **Footer** — `Last updated: hh:mm:ss`, an explicit `refresh`, and `close`. After a failed
  refresh the last good cards stay on screen with `· refresh failed` in the footer; the
  modal only goes hard-error when *nothing* has ever loaded.

### Windows

| Window | Source in the payload | Label |
|---|---|---|
| 5-hour session | `five_hour`, else `session` | `Session (5h)` |
| Weekly | `seven_day` | `Weekly` |
| Weekly, per model | `seven_day_sonnet` / `seven_day_opus` | `Weekly (Sonnet)` / `Weekly (Opus)` |
| Weekly, per model (current shape) | the generic `limits[]` walk — entries with `group: "weekly"` (or a `kind` starting `weekly`), a numeric `percent`, and a `scope.model.display_name` | `Weekly (<model>)` |

The `limits[]` walk is why new models appear without a code change: Anthropic has been
observed shipping per-model weekly usage there while nulling the legacy
`seven_day_opus` / `seven_day_sonnet` keys. A model already emitted by a named key is not
duplicated. An entry without a model name or without a usable percentage is skipped — one
missing row, never a broken response.

Window `key`s are slugs of the label (`Session (5h)` → `session-5h`,
`Weekly (Fable)` → `weekly-fable`) and are stable across refreshes, which is what lets the
hide preferences survive a reload. The estimate card's keys are your
`SWARMERY_USAGE_LIMITS` object keys instead, so the two cards may legitimately share a key
name; preferences are namespaced per provider, so hiding one never hides the other.

Percentages are clamped to 0–100. When the session window arrives without a usable reset
instant, a full 5-hour window is assumed so the row still renders a countdown; this
fallback is deliberately **not** applied to weekly windows, because a fabricated weekly
reset is a lie the operator cannot spot.

## 3. The pace contract — read this before trusting the colours

Pace compares quota spent against window time elapsed, in **percentage points**:

```
delta = percentUsed − percentElapsed
```

with a **±5 percentage-point dead band**. The vocabulary reads backwards at first pass, and
it is the single most misread thing in this surface:

| Delta | Status | Meaning | Colour |
|---|---|---|---|
| `> +5` pt | **`ahead`** | burning quota **FASTER** than time elapses — **over pace, the warning state** | red |
| `−5 … +5` pt | `on-track` | on pace with time elapsed | green |
| `< −5` pt | `behind` | burning **slower** than time elapses — **under pace, the good state** | green |

So `ahead` is bad and `behind` is good. This is not a bug and swapping the colours would
invert the meaning.

Worked examples:

| Used | Elapsed | Delta | Status | Message |
|---|---|---|---|---|
| 28% | 22% | +6 | `ahead` | `6% over pace` |
| 19% | 25% | −6 | `behind` | `6% under pace` |
| 28% | 30% | −2 | `on-track` | `On pace with time elapsed` |
| 60% | 20% | +40 | `ahead` | `40% over pace` |

Pace is **absent** (no `pace` object, no marker, no pace line) when the window has no
forward timing signal — no known reset instant, or a window already past its reset. It is
computed once, server-side, so both cards and both consumers share one definition; nothing
in the frontend re-derives it.

## 4. Security posture

The bearer token is the whole reason this surface needs a security section. The contract:

- **It goes to Anthropic only** — set as an `Authorization` header on the usage request and
  on nothing else. It is the same call the `claude` CLI makes.
- **Never persisted.** No SQLite table, no migration, no cache file. Nothing about usage is
  stored; every read is live or served from a 30-second in-memory cache of the *computed
  response*, which contains no credential material.
- **Never logged.** The credential type's own string form renders `usage.Creds{redacted}`,
  so an accidental `%v` or `%+v` anywhere in the tree prints a constant, not a token.
- **Never served.** No daemon HTTP response carries credential material. Upstream error
  bodies are scrubbed of bearer and `sk-…` material *before* truncation — so a token cannot
  survive as a partial suffix — and capped at 120 characters.
- **Refreshed tokens never reach a CLI source.** A refresh (attempted once on 401/403, and
  up front when the stored token is inside its 60-second expiry grace) is persisted only
  into swarmery's **own** store for a store-owned credential, and is memory-only for a
  credential read from a CLI source — **never written back** to `<configDir>/.credentials.json`
  or the keychain. The single exception to "the daemon does not write CLI files" is the
  **one-time connect handoff** above, which writes a fresh `<configDir>/.credentials.json`
  at most once and never updates it afterwards.
- **`SWARMERY_USAGE_OAUTH=0` is a real off switch.** It short-circuits before any source is
  touched — no file read, no `security` invocation, no keychain prompt — and the card reads
  `usage OAuth disabled (SWARMERY_USAGE_OAUTH=0)`. It covers the write half too: connecting,
  reconnecting and disconnecting all answer `409` while it is set.
- **Disconnect deletes one file, ours.** The card's `disconnect` removes
  `~/.swarmery/credentials/<account>.json` and nothing else. It never touches
  `<configDir>/.credentials.json` or the macOS keychain item — once handed over (or written
  by a CLI login) those are the `claude` CLI's, and a dashboard button that could end your
  terminal login would be a trap. The account simply falls back through the rest of the resolution
  chain, usually to its `Connect` card. It is idempotent: disconnecting an account that is
  already disconnected succeeds. The in-memory refreshed bearer is dropped with the file, so
  the connection cannot outlive it until the next daemon restart.
- **Disconnect does *not* revoke anything at Anthropic.** There is no revocation step in this
  flow: the tokens stay valid upstream until they expire on their own. What ends is *this
  daemon's* use of them. To revoke the authorization itself, do it from your account on
  claude.ai.

## 5. Stability caveat

`/api/oauth/usage` is **undocumented**. It may change shape, start refusing this client, or
be withdrawn, with no notice and no deprecation window. That risk is accepted and contained
rather than hidden:

- the fetch never returns an error — every outcome is encoded as provider `status` + `error`,
  so one broken provider can never break the endpoint or the dashboard;
- every field read is tolerant (several accepted key spellings per field; reset instants
  accepted as RFC3339, unix seconds, or unix millis, as number or numeric string), so a
  field rename costs one row, not the response;
- a body that is not valid JSON produces `Claude usage response was not valid JSON` — an
  error card, not a crash and not a zero rendered as if it were real.

If the endpoint disappears, the honest outcome is a permanent error card. Fix the parser
against a captured payload; do not paper over it with a computed guess.

### What the error cards mean

| Card text | Status | Do this |
|---|---|---|
| `usage OAuth disabled (SWARMERY_USAGE_OAUTH=0)` | not connected | nothing — you opted out |
| ``No Claude credentials — run `claude` to log in`` | not connected | log in with the CLI on this machine |
| ``Claude token missing user:profile scope — re-run `claude` login`` | not connected | re-run the CLI login |
| ``Claude token expired and no refresh token — run `claude` to re-login`` | error | re-run the CLI login |
| ``Claude token refresh failed — run `claude` to re-login`` | error | re-run the CLI login |
| ``Claude auth rejected — run `claude` to re-login`` | error | the token was refused even after one refresh |
| `Claude usage rate-limited (HTTP 429) — retry later` | error | three attempts were spent honouring `Retry-After`; wait |
| `Claude usage request failed: …` | error | network/DNS/TLS; the message is scrubbed of secrets |
| `HTTP <code>: <snippet>` | error | upstream answered something unexpected — likely an endpoint change |
| `Claude usage response was not valid JSON` | error | the payload shape changed; capture it and fix the parser |

The remedy column assumes a CLI login. On a card marked `connectedVia: "swarmery"` every
credential-shaped row above means *reconnect from the card instead* — the hint carries no
command there, because `claude` writes to a store that credential never came from.

## 6. API reference

| Endpoint | Purpose |
|---|---|
| `GET /api/usage` | all provider cards; served from a 30-second process-wide cache |
| `GET /api/usage?fresh=1` | bypass the cache **read** and fetch live (the result still repopulates the cache) |
| `POST /api/usage/accounts/{account}/login/start` | begin an authorization; answers `{authorizeUrl}` and nothing else — the PKCE verifier and CSRF state stay in the daemon |
| `POST /api/usage/accounts/{account}/login/complete` | finish it with `{"code":"<code>#<state>"}`; stores the credential and drops the usage cache |
| `DELETE /api/usage/accounts/{account}/login` | disconnect: removes swarmery's own credential for the account (section 4), drops the usage cache, `{ok:true}` |

The three write endpoints take the same guards: a local origin, an `{account}` the daemon
actually polls (`404` otherwise — including on the DELETE, which is idempotent for a *known*
account but never for an unknown one), and `409` while `SWARMERY_USAGE_OAUTH=0`.

Unscoped — quota is an account-level fact, so there is no `project` parameter. The whole
live fetch is bounded at 20 seconds (individual HTTP calls at 15s), with up to 3 attempts
on 429 honouring a numeric `Retry-After` and otherwise backing off 1s → 2s → 4s.

Provider failures answer **HTTP 200** with an error card. The only non-200s are a `500` for
malformed `SWARMERY_USAGE_LIMITS` and a `500` if the estimate card's token query fails.

```jsonc
{
  "generatedAt": "2026-07-28T14:00:00Z",   // RFC3339, when this body was computed
  "providers": [
    {
      "name": "Claude",
      "status": "ok",                       // "ok" | "error" | "no-auth"
      "plan": "Max",                        // omitted when it cannot be derived
      "source": "oauth",                    // "oauth" | "estimate"
      "windows": [
        {
          "key": "session-5h",
          "label": "Session (5h)",
          "percentUsed": 42,
          "percentLeft": 58,
          "resetText": "resets in 3h 30m",
          "resetMs": 12600000,
          "resetAt": "2026-07-28T17:30:00Z",
          "windowDurationMs": 18000000,
          "pace": {
            "status": "ahead",              // 42% used at 30% elapsed → over pace
            "percentElapsed": 30,
            "message": "12% over pace"
          },
          "source": "oauth"
        },
        {
          "key": "weekly",
          "label": "Weekly",
          "percentUsed": 19,
          "percentLeft": 81,
          "resetText": "resets in 4d 22h",
          "resetMs": 424800000,
          "resetAt": "2026-08-02T12:00:00Z",
          "windowDurationMs": 604800000,
          "pace": { "status": "behind", "percentElapsed": 30, "message": "11% under pace" },
          "source": "oauth"
        },
        {
          "key": "weekly-fable",
          "label": "Weekly (Fable)",
          "percentUsed": 28,
          "percentLeft": 72,
          "resetText": "resets in 4d 22h",
          "resetMs": 424800000,
          "resetAt": "2026-08-02T12:00:00Z",
          "windowDurationMs": 604800000,
          "pace": {
            "status": "on-track",
            "percentElapsed": 30,
            "message": "On pace with time elapsed"
          },
          "source": "oauth"
        }
      ]
    },
    {
      "name": "Telemetry estimate",         // present ONLY when SWARMERY_USAGE_LIMITS is set
      "status": "ok",
      "source": "estimate",
      "windows": [
        {
          "key": "session5h",               // your config key, not a slug
          "label": "5-hour session",
          "percentUsed": 43,
          "percentLeft": 57,
          "resetText": "resets in 3h",
          "resetMs": 10800000,
          "resetAt": "2026-07-28T17:00:00Z",
          "windowDurationMs": 18000000,
          "pace": { "status": "on-track", "percentElapsed": 40, "message": "On pace with time elapsed" },
          "source": "estimate",
          "used": 21500000,                 // estimate card only: raw token counts
          "limit": 50000000
        },
        {
          "key": "weekly",
          "label": "Weekly",
          "percentUsed": 32,
          "percentLeft": 68,
          "resetText": "resets in 1d 10h",
          "resetMs": 122400000,
          "resetAt": "2026-07-30T00:00:00Z",
          "windowDurationMs": 604800000,
          "pace": { "status": "behind", "percentElapsed": 80, "message": "48% under pace" },
          "source": "estimate",
          "used": 96000000,
          "limit": 300000000
        }
      ]
    }
  ]
}
```

A provider that failed carries `error` instead of populated `windows`:

```json
{ "name": "Claude", "status": "no-auth", "source": "oauth",
  "error": "No Claude credentials — run `claude` to log in", "windows": [] }
```

Field notes: `resetText`, `resetMs`, `resetAt`, `windowDurationMs` and `pace` are all
omitted when unknown; `used` / `limit` appear on the estimate card only; the old top-level
`configured` flag no longer exists — the estimate card's presence carries that information.
A provider also carries `connectedVia: "swarmery"` when its credential came from swarmery's
own store, on a healthy card and on a failing one alike — provenance only, never a token and
never a path. Absent means the credential came from one of the CLI's sources, or that there
is none.

## 7. Cadences

One poller serves both the chip and the modal, because every cache miss costs a real
upstream call and two timers against one endpoint would double that rate for no extra
information.

| When | Cadence |
|---|---|
| chip mounted, modal closed | **120s** |
| modal open | **30s** (matches the daemon's own cache TTL) |
| browser tab hidden | **paused entirely** — no timer exists |
| tab becomes visible again | **one** catch-up fetch, and only if the snapshot already outlived the cadence in force; a quick tab-away costs nothing |
| modal opened | **one** fetch, and only if the snapshot is already older than 5s — so opening the modal twice in quick succession costs one call, not two |
| page load | one fetch on mount |
| `refresh` button | immediate, and the **only** thing that sends `?fresh=1` |

Automatic polls never send `?fresh=1` — they are absorbed by the 30-second daemon cache, so
the sustained upstream rate is at most one call per 30 seconds no matter how many tabs are
open. Opening the modal both changes the shared cadence (reference-counted, so a double mount
cannot leave it stuck fast) *and* takes the staleness-gated fetch above: changing a
`setInterval`'s delay does not make it fire, so without that fetch the panel would open
showing whatever the slow 120s cadence last retrieved. Closing the modal never fetches. The
modal separately ticks a display-only clock every 30s so `resets in …` stays live between
polls.

## 8. Operator knobs

| Variable | Effect |
|---|---|
| `SWARMERY_USAGE_OAUTH=0` | disables the credential read entirely — no file, no keychain, no upstream call. The card says so. |
| `CLAUDE_CONFIG_DIR` | first credential source directory, ahead of `~/.claude` |
| `SWARMERY_USAGE_LIMITS` | JSON quota config; its presence is what emits the `Telemetry estimate` card |

Checking it by hand:

```bash
curl -s localhost:7777/api/usage | jq                    # cached (≤30s old)
curl -s 'localhost:7777/api/usage?fresh=1' | jq          # live upstream call
SWARMERY_USAGE_OAUTH=0 ./swarmery serve                  # verify the opt-out path
```

## 9. Why `/usage` in my terminal shows a different account

The project → account binding governs what swarmery **dispatches**: runs started from the
dashboard, and the dashboard's embedded terminal. A `claude` you start by hand in your own
shell knows nothing about it — without `CLAUDE_CONFIG_DIR` set in that shell, the CLI reads
`~/.claude`, i.e. the machine's **default** account. So `/usage` in a hand-opened terminal
can honestly disagree with the account the dashboard says this project runs under; neither
is lying, they are answering different questions.

To run a hand-started CLI under the project's account, from the project root:

```bash
swarmery account which            # which account this project runs under, and why
swarmery account exec -- claude   # run one command under that account
eval "$(swarmery account env)"    # or export CLAUDE_CONFIG_DIR into this shell
```

`which|use|clear|env|exec` never contact the daemon, so they keep working with swarmery
stopped. If `which` reports `source: default` although the file names an account, check the
warning on stderr. A `settings.local.json` that git tracks is ignored on purpose (see
*Account binding* in the concepts doc). Untrack it with `git rm --cached` and gitignore it. The accounts pack's `/account setup-shell` installs a shell wrapper that applies
the binding automatically per directory.
