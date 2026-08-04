#!/bin/bash
#
# Applies GitW3 branding onto the working tree, in place, immediately before `make build`.
#
# Why a build-time overlay rather than committed changes to upstream files:
#
#   Forgejo does support runtime overrides through `custom/` (see modules/options/base.go), but
#   the Docker image sets GITEA_CUSTOM=/data/gitea and /data is a VOLUME — so the repository's
#   `custom/` directory is never read in a container. Assets under options/ and public/ are
#   embedded into the binary at build time via the `bindata` tag, so overlaying them just before
#   the build is the only approach that brands the artefact we actually ship.
#
#   Keeping the branding here instead of in tracked upstream files means our diff against Forgejo
#   stays empty, and merging upstream security releases stays conflict-free.
#
# Consequence: a plain `make build` produces an UNBRANDED binary. Run this first. CI does.
#
# Idempotent — safe to run twice.
#
set -euo pipefail

BRAND="GitW3"
BRAND_LC="gitw3"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BRANDING="$ROOT/branding"
cd "$ROOT"

log() { printf '  %s\n' "$*"; }
die() { printf 'apply-branding: %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 1. Strings compiled into the binary that no configuration can override.
#
# Each entry is file|old|new. If `old` is missing AND `new` is absent, we abort: upstream
# changed the string and the branding would otherwise silently stop applying after a merge.
# That failure must be loud — a half-branded release is worse than a failed build.
# ---------------------------------------------------------------------------
patch_literal() {
    local file="$1" old="$2" new="$3"

    [ -f "$file" ] || die "no such file: $file (upstream layout changed?)"

    if grep -qF -- "$new" "$file"; then
        log "already applied: $file"
        return
    fi
    grep -qF -- "$old" "$file" || die "expected string not found in $file:
    $old
  Upstream probably changed it. Update branding/apply.sh to match."

    OLD="$old" NEW="$new" python3 - "$file" <<'PY'
import os, sys
path = sys.argv[1]
with open(path, encoding="utf-8") as fh:
    body = fh.read()
with open(path, "w", encoding="utf-8") as fh:
    fh.write(body.replace(os.environ["OLD"], os.environ["NEW"]))
PY
    log "patched: $file"
}

echo "Applying $BRAND branding"

# The CLI binary name, shown by `--version` and in every help screen.
patch_literal cmd/main.go \
    'app.Name = "forgejo"' \
    "app.Name = \"$BRAND_LC\""

# The default instance name: browser title, home page, and every page header when the
# operator has not set APP_NAME in app.ini.
patch_literal modules/setting/server.go \
    'MustString("Forgejo: Beyond coding. We Forge.")' \
    "MustString(\"$BRAND\")"

# Startup log line.
patch_literal cmd/web.go \
    'log.Info("Forgejo version: %s%s"' \
    "log.Info(\"$BRAND version: %s%s\""

# ---------------------------------------------------------------------------
# 2. User-facing translation strings.
#
# A token substitution rather than a maintained parallel locale file: upstream adds and edits
# strings every release, and a parallel file would drift out of sync silently. A substitution
# re-applies itself to whatever upstream currently ships.
#
# Keys listed in branding/locale-keep.txt are left alone — they refer to the upstream Forgejo
# project or to external infrastructure genuinely named Forgejo.
# ---------------------------------------------------------------------------
[ -f "$BRANDING/locale-keep.txt" ] || die "missing $BRANDING/locale-keep.txt"

BRAND="$BRAND" BRAND_LC="$BRAND_LC" KEEP="$BRANDING/locale-keep.txt" \
python3 - options/locale/locale_en-US.ini <<'PY'
import os, re, sys

path = sys.argv[1]
brand, brand_lc = os.environ["BRAND"], os.environ["BRAND_LC"]

keep = set()
with open(os.environ["KEEP"], encoding="utf-8") as fh:
    for line in fh:
        line = line.strip()
        if line and not line.startswith("#"):
            keep.add(line)

# Never rewrite these: they are addresses of upstream infrastructure, not brand mentions.
PROTECTED = ("forgejo.org", "codeberg.org/forgejo", "forgejo-runner")

def rebrand(text):
    holes = {}
    for i, token in enumerate(PROTECTED):
        marker = f"\x00{i}\x00"
        if token in text:
            holes[marker] = token
            text = text.replace(token, marker)
    text = text.replace("Forgejo", brand).replace("forgejo", brand_lc)
    for marker, token in holes.items():
        text = text.replace(marker, token)
    return text

out, changed = [], 0
for line in open(path, encoding="utf-8"):
    # Only ever rewrite the VALUE of a `key = value` line. Keys are identifiers the Go code
    # looks up (`return_to_forgejo`), so rebranding one silently breaks the string it names.
    # Section headers and comments are left alone for the same reason.
    m = re.match(r"^([A-Za-z0-9_.\-]+)(\s*=\s*)(.*)$", line, re.S)
    if not m:
        out.append(line)
        continue
    key, sep, value = m.groups()
    if key in keep:
        out.append(line)
        continue
    new = key + sep + rebrand(value)
    changed += new != line
    out.append(new)

with open(path, "w", encoding="utf-8") as fh:
    fh.writelines(out)
print(f"  rebranded {changed} locale lines ({len(keep)} keys kept as Forgejo)")
PY

# ---------------------------------------------------------------------------
# 3. Logo and icons.
#
# PLACEHOLDER assets. Forgejo's own logo is CC BY-SA 4.0 with an attribution exemption granted
# only to the Forgejo project, so the real GitW3 mark must be original work — never a derivative.
# ---------------------------------------------------------------------------
for asset in logo.svg logo.png favicon.svg favicon.png apple-touch-icon.png; do
    src="$BRANDING/assets/$asset"
    [ -f "$src" ] || die "missing branding asset: $src"
    cp "$src" "public/assets/img/$asset"
done
log "replaced 5 image assets"

echo "$BRAND branding applied."
