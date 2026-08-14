# GitW3

GitW3 is MetaState's self-hosted Git forge. It is a fork of [Forgejo](https://forgejo.org/),
rebranded for the MetaState / W3DS ecosystem.

## Relationship to Forgejo

GitW3 tracks Forgejo releases. Everything that makes GitW3 *GitW3* is deliberately confined to a
thin layer on top of upstream:

- **Branding** — application name, logo, colour scheme, templates and locale strings. Applied by
  [`branding/apply.sh`](branding/apply.sh), which rewrites upstream files in the working tree
  immediately before `make build`. Those rewrites are never committed, so the tracked diff against
  Forgejo stays empty. Forgejo's own `custom/` mechanism cannot do this job: the Docker image
  points `GITEA_CUSTOM` at a volume, so the repository's `custom/` directory is never read in a
  container, and the assets that need overriding are embedded into the binary at build time by the
  `bindata` tag anyway.
- **Nothing else, for now.** In particular, W3DS login is *not* implemented in this fork. It is
  provided by a separate OIDC bridge service and wired up through Forgejo's built-in OAuth2
  authentication sources, so it needs no change to this codebase.

Keeping the patch surface near zero is the whole strategy. Every line we diverge from upstream is
a line that can conflict when the next Forgejo security release has to be merged.

Note that the Go module path is `forgejo.org` and the built binary is named `gitea` upstream. We
override the binary name at build time (`EXECUTABLE=gitw3`) and leave both alone in the source —
renaming them for real would mean touching thousands of lines for no user-visible gain.

## Branches

| Branch | Owner | Purpose |
| --- | --- | --- |
| `main` | us | GitW3. Branched from upstream `v16.0.2`; carries our commits. |
| `forgejo` | upstream | Forgejo's development branch, mirrored verbatim. Do not commit here. |
| `v*/forgejo` | upstream | Forgejo release branches, mirrored verbatim. Do not commit here. |
| `v*` (tags) | upstream | Forgejo release tags, mirrored verbatim. |
| `gitw3-v*` (tags) | us | GitW3 releases. |

Upstream refs are refreshed daily by [`.github/workflows/upstream-sync.yml`](.github/workflows/upstream-sync.yml),
which only ever writes to upstream-owned refs and never to `main`.

## Keeping up with upstream

See **[docs/gitw3/upstream-sync.md](docs/gitw3/upstream-sync.md)** for the merge procedure.

This is not optional maintenance. Forgejo ships security releases regularly — v16.0.2 and v15.0.6
both landed on 2026-07-30 — and an un-upgraded forge exposed to the internet is a liability.

## Building

Requires Go (version pinned in [`go.mod`](go.mod)) and Node (pinned in
[`.node-version`](.node-version)).

```sh
make deps-frontend
./branding/apply.sh
EXECUTABLE=gitw3 TAGS="bindata sqlite sqlite_unlock_notify" make build
./gitw3 --version
git checkout -- cmd/ docker/ modules/ options/ public/ routers/ services/ templates/ web_src/
```

The branding step is not optional: without it `make build` produces a binary that still calls
itself Forgejo. The last line puts the working tree back — see
[`branding/README.md`](branding/README.md).

Container images are published to `ghcr.io/ensombl/gitw3`.

## Local development

For iterating on code, use the live-reload dev server instead of the full build above:

```sh
make deps-frontend
TAGS="sqlite sqlite_unlock_notify" GITEA_RUN_MODE=dev make watch
```

`make watch` runs the frontend (`webpack --watch`) and backend (`air`, which rebuilds and restarts
on `.go`/`.tmpl` changes) together. `TAGS` must be set explicitly here and include `sqlite`: unlike
the release build above, `make watch`'s backend target does not set it, so without it the install
wizard won't offer SQLite3 as a database option.

First run serves the install wizard at `http://localhost:3000`. If that port is taken, set
`[server] HTTP_PORT` in `custom/conf/app.ini` — environment variable overrides
(`GITEA__server__HTTP_PORT`) are not read this early, only `app.ini` is. SQLite needs no other setup.

### Testing W3DS login locally

W3DS login isn't part of this codebase (see above) — it's a separate OIDC bridge wired in as a
Forgejo OAuth2 authentication source. To exercise it locally:

1. Run the OIDC bridge service (out of scope for this repo) and note its client ID, client secret,
   and `.well-known/openid-configuration` discovery URL.
2. Register it as an authentication source. The name must be exactly `W3DS` — it's baked into the
   bridge's redirect URI as `<ROOT_URL>/user/oauth2/W3DS/callback`:
   ```sh
   ./gitea admin auth add-oauth \
     --name "W3DS" \
     --provider "openidConnect" \
     --key "<client id>" \
     --secret "<client secret>" \
     --auto-discover-url "http://<bridge-host>/.well-known/openid-configuration" \
     --scopes "openid" --scopes "profile" --scopes "email"
   ```
   The `profile`/`email` scopes are needed so Forgejo gets a real username/email back instead of
   falling back to the OIDC `sub` claim and a synthetic `@w3ds.invalid` address.
3. Add the following to `custom/conf/app.ini` and restart (`app.ini` is only read at process
   startup, so this needs a restart of `make watch`, not just a hot-reload):
   ```ini
   [oauth2_client]
   ENABLE_AUTO_REGISTRATION = true
   ACCOUNT_LINKING = login
   USERNAME = nickname
   REGISTER_EMAIL_CONFIRM = false
   ```
   Without `ENABLE_AUTO_REGISTRATION`, Forgejo shows a manual "Complete new account" step on every
   first-time OAuth2 login instead of silently provisioning the account, which is the intended
   production behaviour.

## Licence

Forgejo is GPL-3.0-or-later, and so is GitW3. See [LICENSE](LICENSE).

The Forgejo *branding* is not covered by that licence and is not ours to reuse: the logo is
CC BY-SA 4.0 by [Caesar Schinas](https://caesarschinas.com/), and the Jo mascot is CC BY 4.0 by
[David Revoy](https://www.peppercarrot.com/). The attribution exemption on the logo is granted to
the Forgejo project only. **GitW3 branding assets must therefore be original work, not derivatives
of Forgejo's.**

## Upstream

- Source: <https://codeberg.org/forgejo/forgejo>
- Documentation: <https://forgejo.org/docs/latest/>
