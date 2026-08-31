# GitW3 branding

Everything in this directory belongs to us. Nothing here exists upstream, so nothing here can
conflict when a Forgejo release is merged.

Most branding is **applied at build time** by [`apply.sh`](apply.sh). See the header of that script
for why — the short version is that the Docker image sets `GITEA_CUSTOM=/data/gitea` on a volume,
so Forgejo's runtime `custom/` override mechanism never sees the repository's own `custom/`
directory, and assets are embedded into the binary at build time anyway.

The purple GitW3 palette is intentionally source-controlled in
`web_src/css/themes/theme-forgejo-light.css` and `theme-forgejo-dark.css`. This is the one branding
change that a clean build must retain without an overlay. [`check-theme.sh`](check-theme.sh) checks
the complete light and dark scales and rejects the old duplicate-`:root` cascade approach.

A plain `make build` therefore keeps the GitW3 colours but does not apply the name, locale, and
asset substitutions. Run the script first for a fully branded artefact:

```sh
./branding/apply.sh
EXECUTABLE=gitw3 TAGS="bindata sqlite sqlite_unlock_notify" make build
git checkout -- cmd/ docker/ modules/ options/ public/ routers/ services/ templates/   # restore the tree afterwards
```

The script rewrites tracked upstream files in place — around a hundred of them. **Never commit
those generated changes.** The two theme-source changes are the deliberate exception and should
remain committed. The script prints the restore command when it finishes.

CI does this automatically and asserts on the result, so an unbranded artefact fails the build
rather than shipping quietly.

## Contents

| Path | Purpose |
| --- | --- |
| `assets/` | Logo and icons, copied over `public/assets/img/` |
| `check-theme.sh` | Verifies the source and compiled light/dark GitW3 palettes |
| `locale-keep.txt` | Locale keys that must keep the Forgejo name, with the reason for each |

## Assets are placeholders

`assets/` currently holds a **placeholder mark**, not the final GitW3 logo.

### The house style

The mark follows the convention used by the other MetaState platform logos (see
`infrastructure/eid-wallet/static/images/Logo-*.svg` in the `prototype` repo): a rounded square
filled with a brand colour, holding one simple white line icon.

| | |
| --- | --- |
| Canvas | `viewBox="0 0 162 162"` |
| Background | `<rect width="162" height="162" rx="32" fill="…"/>` — `rx` is 15–23% of the side across the family |
| Icon | white, `stroke-width="9"`, `stroke-linecap`/`stroke-linejoin` `round` |
| Safe area | roughly x,y 41 → 121, so the icon reads at favicon size |
| Colour | `#8968FF`, taken from the main W3DS logo |

### Regenerating

Replace `logo.svg`, then:

```sh
./branding/render-assets.sh
```

**Do not rasterise with ImageMagick alone.** `magick logo.svg logo.png` appears to succeed but
silently produces a blank square: ImageMagick falls back to its built-in MSVG renderer when
librsvg is absent, and MSVG draws neither strokes nor circles. `render-assets.sh` uses headless
Chrome instead and refuses to finish if a raster comes out as a flat fill.

`loading.svg` is the animated variant shown on the install and migration pages; keep it visually
consistent with `logo.svg` by hand — it is not generated.

**The GitW3 mark must be original work.** Forgejo's logo is CC BY-SA 4.0 (Caesar Schinas) with an
attribution exemption granted only to the Forgejo project, and the Jo mascot is CC BY 4.0 (David
Revoy). Deriving ours from theirs would carry those obligations. The *code* is GPLv3 and forking
it is explicitly permitted — the branding is the part that is not ours to reuse.

## Adding a string to the keep list

The locale pass substitutes `Forgejo` → `GitW3` in every translation **value**, never in a key
(keys are identifiers the Go code looks up; rewriting one silently breaks the string it names).

Some strings must keep the Forgejo name because they genuinely refer to the upstream project or
to external infrastructure — bug reports, the update checker's DNS record, `forgejo-runner`. Add
the locale key to `locale-keep.txt` with a comment explaining why. Substituting these is not a
cosmetic slip: it sends our users to the wrong issue tracker, or tells them to install software
that does not exist.
