# Keeping GitW3 in sync with Forgejo

GitW3 is a long-lived fork. This document is the procedure for pulling upstream Forgejo changes
in, and the reasoning behind it.

## The model

```
codeberg.org/forgejo/forgejo          github.com/ensombl/gitw3
─────────────────────────────         ────────────────────────────────────
  forgejo        ──── daily ────▶     forgejo          (upstream, verbatim)
  v16.0/forgejo  ──── mirror ───▶     v16.0/forgejo    (upstream, verbatim)
  v16.0.2 (tag)  ──────────────▶      v16.0.2 (tag)

                                      main             (ours: branding commits)
                                        └─ merge v16.0.3 when it lands
                                      gitw3-v16.0.3    (our release tag)
```

Two rules make this work:

1. **Upstream refs are read-only for us.** We never commit to `forgejo` or `v*/forgejo`. The sync
   workflow force-updates them from Codeberg and would silently discard anything we put there.
2. **We merge, we never rebase.** Rebasing our commits onto each new release would rewrite history
   every time, breaking every existing clone and every open branch. A merge keeps `main` append-only.

## Automated part

[`.github/workflows/upstream-sync.yml`](../../.github/workflows/upstream-sync.yml) runs daily at
04:00 UTC (and on demand via *Run workflow*). It:

- fetches `forgejo` and `v*/forgejo` branches plus all tags from Codeberg,
- force-pushes them into this repo under their original names,
- opens a tracking issue when the newest upstream stable tag is not yet an ancestor of `main`.

It deliberately does **not** touch `main` and does **not** attempt an automatic merge. Merging
someone else's release into a branded fork needs a human looking at the conflicts.

The workflow pushes with `GITHUB_TOKEN`, so the refs it writes do not trigger other workflows.
That is intentional — mirroring 296 upstream tags should not fire 296 release builds.

### Never mirror with `--mirror`

Two traps, both hit during the initial import, both avoided by using explicit refspecs
(`refs/heads/*` and `refs/tags/*`) instead of `git push --mirror`:

- **`--mirror` deletes.** It makes the remote match the source exactly, so it would delete `main`
  — which exists only here, never upstream.
- **Codeberg exposes far more than branches and tags.** A `git clone --mirror` of upstream also
  brings down ~10,700 `refs/pull/*` refs and ~1,000 `refs/soft-fork/*` refs. One of them,
  `refs/pull/12411/head`, contains a 129 MB committed test binary (`tests/integration/gitea`).
  GitHub rejects any push whose pack contains a file over 100 MB, and because a push is checked
  as a single pack, that one blob makes the *entire* push fail with a confusing
  `pre-receive hook declined` attributed to unrelated tags.

The blob is not reachable from `forgejo` or from any release tag, so excluding `refs/pull/*`
costs us nothing. Do not try to fix this by rewriting history: stripping the blob would change
every downstream commit SHA and destroy the merge relationship with upstream, which is the entire
point of the fork.

## Merging a new upstream release

Say Forgejo has released `v16.0.3`.

```sh
git fetch origin --tags
git switch main
git switch -c chore/merge-forgejo-v16.0.3

git merge v16.0.3
```

Resolve conflicts, then:

```sh
make deps-frontend
EXECUTABLE=gitw3 TAGS="bindata sqlite sqlite_unlock_notify" make build
./gitw3 --version
```

Open a PR against `main`. Once merged, cut the release:

```sh
git switch main && git pull
git tag gitw3-v16.0.3
git push origin gitw3-v16.0.3     # triggers .github/workflows/release.yml
```

### Where conflicts will actually happen

Our diff against upstream is small on purpose, so the blast radius is predictable:

| File | Why it conflicts | Difficulty |
| --- | --- | --- |
| `README.md` | We replaced it wholesale | Trivial — keep ours |
| `.github/workflows/*` | Upstream has no `.github/`, so never | None |
| `docs/gitw3/*` | Ours alone, upstream has no `docs/` | None |
| Branding assets under `custom/` | Ours alone | None |
| Overridden templates | Only if upstream edits the same template | Real, but rare |

If a merge ever starts producing conflicts outside this table, that is a signal that our patch
surface has grown beyond the intended thin layer, and it is worth asking whether the change
belongs in a customisation layer instead.

## Which upstream line to follow

Forgejo maintains several release lines in parallel (v16.0, v15.0, …) and backports security
fixes to the supported ones. GitW3 follows the **latest stable line**. Upgrading across a major
line (v16 → v17) is a deliberate, planned merge, not a routine one — check the upstream
[release notes](https://codeberg.org/forgejo/forgejo/src/branch/forgejo/RELEASE-NOTES.md) for
breaking changes first.

## Service level

Treat upstream security releases as time-critical. The practical commitment is: merge, build and
redeploy within a few days of a security release landing. If nobody owns that, the fork should not
be running in production.
