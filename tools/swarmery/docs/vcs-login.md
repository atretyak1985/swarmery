# Signing the daemon in to a code host

Landing a phase (push, open a PR/MR) needs the daemon to act on the project's
code host as you. It can use the host CLI's own login (`gh auth login` /
`glab auth login` on the daemon's machine) — or a token the daemon holds
itself, which you give it from the dashboard. This page covers the second way.

When the project banner says *not signed in* or *sign-in expired*, **Sign in**
opens a dialog with two tabs:

- **Device code** — the daemon asks the host for a one-time code; you enter it
  at the host's verification page, in any browser, on any machine. Works for a
  daemon on a remote host. Needs an OAuth application registered once per host
  (below).
- **Token** — paste a personal access token. Needs nothing registered. The
  daemon checks the token with the host first and stores it only when the host
  accepts it.

The dialog's footer also names the terminal command (`gh auth login …` /
`glab auth login …`) for signing the CLI in on the daemon's machine instead.

## Registering the OAuth application (device code only)

Client ids are public identifiers, not secrets: the device flow uses no client
secret. Register the application once per host, then hand the daemon its
client id.

### GitHub (github.com or GitHub Enterprise Server)

1. On the host: **Settings → Developer settings → OAuth Apps → New OAuth App**
   (for an organisation-owned app, the organisation's settings).
2. Any name; *Homepage URL* and *Authorization callback URL* are required by
   the form but unused by the device flow — `http://localhost:7777` is fine.
3. Create it, then tick **Enable Device Flow** and save.
4. Copy the **Client ID**. Do not generate a client secret.

The daemon requests the `repo` scope (push a branch, open a pull request).

### GitLab (gitlab.com or self-managed)

1. On the host: **User settings → Applications → Add new application** (a group
   or an instance-wide application works the same).
2. Any name; *Redirect URI* is required by the form but unused —
   `http://localhost:7777` is fine.
3. **Confidential: off** (the device flow is a public-client grant).
4. Scope **`api`** only.
5. Save and copy the **Application ID** — that is the client id.

Self-managed GitLab needs a version with the OAuth 2.0 device authorization
grant (introduced in GitLab 17.2). If starting the device flow fails on your
instance, use the Token tab.

**GitLab device tokens expire after about two hours**, and the daemon does not
store the refresh token yet — after that the banner reports *sign-in expired*
again. For GitLab, prefer the **Token** tab with a personal access token
(**User settings → Access tokens**, scope `api`, an expiry you choose).

## Giving the daemon the client id

Per host, first match wins:

1. `swarmery.vcs.clientIds.<host>` in the project's `.claude/settings.local.json`
   — read on every sign-in, no restart needed:

   ```jsonc
   { "swarmery": { "vcs": { "clientIds": {
     "github.com": "Iv1.0123456789abcdef",
     "gitlab.example.com": "f1e2d3c4b5a6…"
   } } } }
   ```

2. The daemon's environment — set it where the service is defined (launchd
   plist / systemd unit, see the README) and restart the daemon:

   | Variable | Used for |
   |---|---|
   | `SWARMERY_GITHUB_CLIENT_ID` | every GitHub host without a per-host override |
   | `SWARMERY_GITLAB_CLIENT_ID` | every GitLab host without a per-host override |

Without either, the Device code tab answers *device flow unconfigured* and
names the variable to set; the Token tab keeps working.

## Where the token is stored

`~/.swarmery/secrets/vcs-<host>.env` (the directory follows
`SWARMERY_SECRETS_DIR` when it is set), mode `0600` in a `0700` directory — a
store file or directory open to group or others is refused, not read. The file
holds `GH_TOKEN=…` (GitHub) or `GITLAB_TOKEN=…` (GitLab).

Every `gh`/`glab` call the daemon makes for that host then runs with that token
and with an isolated CLI config directory under the secrets directory, so the
daemon neither reads nor rewrites your own CLI login. On a GitHub Enterprise
Server host the token is also exported as `GH_ENTERPRISE_TOKEN`, the variable
`gh` reads there.

The token is never echoed back to the browser and never written to the log;
the device code never leaves the daemon. Pending device sign-ins live in the
daemon's memory only (at most 10 per project) and are lost on restart — start
the sign-in again.

## Revoking

Make the daemon forget its token for a project's host — its CLI calls fall back
to your own CLI login, if any:

```bash
curl -X DELETE http://localhost:7777/api/projects/<projectId>/vcs/token
# or
rm ~/.swarmery/secrets/vcs-<host>.env
```

That only deletes the daemon's copy. To invalidate the token itself, revoke it
on the host: GitHub **Settings → Applications → Authorized OAuth Apps** (device
flow) or **Settings → Developer settings → Personal access tokens**; GitLab
**User settings → Applications → Authorized applications** or **Access
tokens**.

## API

All routes refuse a request carrying a browser `Origin` other than the
dashboard's own (403).

| Request | Answer |
|---|---|
| `POST /api/projects/{id}/vcs/login` `{"method":"device"}` | `202 {loginId, userCode, verificationUri, verificationUriComplete?, expiresIn, interval}`; `409 device-flow-unconfigured` (with `hint`, `envVar`) / `device-flow-disabled`; `422 no-remote` / `provider-unknown`; `429 too-many-pending-logins`; `502 device-flow-failed` |
| `GET /api/projects/{id}/vcs/login/{loginId}` | one poll step: `200 {status: pending\|ok\|expired\|denied, login?, interval}` — call it every `interval` seconds (the answer's interval wins); on `ok` the token is stored; `404 login-not-found` |
| `POST /api/projects/{id}/vcs/login` `{"method":"token","token":"…"}` | `200 {status:"ok", login}`; `422 not-authenticated` (rejected — nothing stored); `502 token-unverified` (could not check — nothing stored) |
| `DELETE /api/projects/{id}/vcs/token` | `204`, also when there was nothing to delete |

A successful sign-in or delete drops the project's cached `GET …/vcs` answer, so
the banner's next read reflects it.
