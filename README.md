# GitW3

GitW3 is MetaState's self-hosted Git forge. It is a fork of [Forgejo](https://forgejo.org/),
rebranded for the MetaState / W3DS ecosystem.

## Relationship to Forgejo

GitW3 tracks Forgejo releases. Everything that makes GitW3 *GitW3* is deliberately confined to a
thin layer on top of upstream:

- **Branding** — application name, logo, colour scheme, and any overridden templates. Applied
  through the customisation mechanisms Forgejo already provides (`APP_NAME` in `app.ini`,
  `custom/public/assets/`, `custom/templates/`) rather than by editing upstream source.
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
EXECUTABLE=gitw3 TAGS="bindata sqlite sqlite_unlock_notify" make build
./gitw3 --version
```

Container images are published to `ghcr.io/ensombl/gitw3`.

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
