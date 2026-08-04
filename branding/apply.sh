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

# The container generates its app.ini from these init scripts, and their APP_NAME default wins
# over the Go one above. The browser title comes from here, not from server.go — patching only
# the Go default leaves every containerised instance branded "Forgejo".
patch_literal docker/root/etc/s6/gitea/setup \
    'APP_NAME=${APP_NAME:-"Forgejo: Beyond coding. We forge."}' \
    "APP_NAME=\${APP_NAME:-\"$BRAND\"}"

patch_literal docker/rootless/usr/local/bin/docker-setup.sh \
    'APP_NAME=${APP_NAME:-"Forgejo: Beyond coding. We forge."}' \
    "APP_NAME=\${APP_NAME:-\"$BRAND\"}"

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
python3 - <<'PY'
import glob, json, os, re, sys

brand, brand_lc = os.environ["BRAND"], os.environ["BRAND_LC"]

# A trailing `*` in the keep list matches by prefix, for families of related keys.
exact, prefixes = set(), []
with open(os.environ["KEEP"], encoding="utf-8") as fh:
    for line in fh:
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        prefixes.append(line[:-1]) if line.endswith("*") else exact.add(line)

def kept(key):
    return key in exact or any(key.startswith(p) for p in prefixes)

# Never rewrite these: they are addresses of upstream infrastructure or the name of an external
# binary, not brand mentions.
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

# Forgejo carries two locale systems: the original `key = value` .ini files and the newer
# `"key": "value",` JSON ones under locale_next/. Branding only the first leaves half the UI
# in Forgejo's name — including the root-URL warning and every theme name.
#
# Both are rewritten line by line so upstream's exact formatting survives, and only the value
# side is ever touched: keys are identifiers the Go code looks up, so rebranding
# `return_to_forgejo` silently breaks the string it names.
INI_LINE = re.compile(r"^([A-Za-z0-9_.\-]+)(\s*=\s*)(.*)$", re.S)
JSON_LINE = re.compile(r'^(\s*")([^"]+)("\s*:\s*)(.*)$', re.S)

def process(path, pattern, key_at, value_at):
    out, changed = [], 0
    for line in open(path, encoding="utf-8"):
        m = pattern.match(line)
        if not m or kept(m.group(key_at + 1)):
            out.append(line)
            continue
        groups = m.groups()
        new = "".join(groups[:value_at]) + rebrand(groups[value_at])
        changed += new != line
        out.append(new)
    if changed:
        with open(path, "w", encoding="utf-8") as fh:
            fh.writelines(out)
    return changed

ini_paths = sorted(glob.glob("options/locale/locale_*.ini"))
json_paths = sorted(glob.glob("options/locale_next/locale_*.json"))

# Guard on the files being *found*, not on them being *changed* — a second run legitimately
# rewrites nothing, and treating that as an error would break idempotency.
if not ini_paths or not json_paths:
    sys.exit("apply-branding: locale files are not where expected — upstream layout changed")

total = files = 0
for path in ini_paths:
    n = process(path, INI_LINE, 0, 2)
    total, files = total + n, files + (n > 0)

for path in json_paths:
    n = process(path, JSON_LINE, 1, 3)
    total, files = total + n, files + (n > 0)
    # A malformed rewrite would otherwise only surface as a broken UI at runtime.
    try:
        with open(path, encoding="utf-8") as fh:
            json.load(fh)
    except Exception as exc:
        sys.exit(f"apply-branding: {path} is no longer valid JSON after rebranding: {exc}")

print(f"  rebranded {total} strings across {files} locale files "
      f"({len(exact) + len(prefixes)} keys kept as Forgejo)")
PY

# ---------------------------------------------------------------------------
# 3. Logo and icons.
#
# PLACEHOLDER assets. Forgejo's own logo is CC BY-SA 4.0 with an attribution exemption granted
# only to the Forgejo project, so the real GitW3 mark must be original work — never a derivative.
# ---------------------------------------------------------------------------
# `source:destination` — the loading indicator keeps its upstream filename because templates
# reference it by that path.
#
# public/assets/img/forgejo.svg is deliberately NOT replaced: it is the icon for the Forgejo
# webhook type, which we keep labelled "Forgejo" because its identifier in config and in the
# API is `forgejo`. Same reasoning as settings.web_hook_name_forgejo in locale-keep.txt.
count=0
while IFS=: read -r src dest; do
    [ -n "$src" ] || continue
    from="$BRANDING/assets/$src"
    [ -f "$from" ] || die "missing branding asset: $from"
    [ -f "public/assets/img/$dest" ] || die "upstream asset public/assets/img/$dest is gone — \
branding would silently add an unused file instead of replacing anything"
    cp "$from" "public/assets/img/$dest"
    count=$((count + 1))
done <<'ASSETS'
logo.svg:logo.svg
logo.png:logo.png
favicon.svg:favicon.svg
favicon.png:favicon.png
apple-touch-icon.png:apple-touch-icon.png
loading.svg:forgejo-loading.svg
ASSETS
log "replaced $count image assets"

echo "$BRAND branding applied."
