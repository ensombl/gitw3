#!/usr/bin/env bash
# Keeps the GitW3 host's disk from filling up with what deploys leave behind:
# source checkouts and images of superseded GitW3 builds, BuildKit cache, and
# old config backups. The running build and the newest KEEP builds (for
# rolling back) are always kept.
#
# Expects the compose layout of /opt/gitw3: images tagged gitw3:<commit> and
# gitw3-platform-manifest-sync:<commit>, checkouts in source-<commit prefix>.
set -euo pipefail

dir=${GITW3_DIR:-/opt/gitw3}
keep_builds=${GITW3_TIDY_KEEP:-3}
keep_backups=${GITW3_TIDY_KEEP_BACKUPS:-10}
# With the containerd image store, --max-used-space removes nothing; age works.
cache_age=${GITW3_TIDY_CACHE_AGE:-24h}
# GITW3_TIDY_DRY_RUN=1 prints what would be removed.
dry_run=${GITW3_TIDY_DRY_RUN:-}
cd "$dir"

run() {
	if [ -n "$dry_run" ]; then
		echo "would run: $*"
	else
		"$@"
	fi
}

running=$(docker compose ps --format '{{.Image}}' | sed -n 's/^gitw3:\(.*\)$/\1/p' | head -n 1)
if [ -z "$running" ]; then
	echo "GitW3 is not running; leaving everything in place" >&2
	exit 1
fi
mapfile -t keep < <({
	echo "$running"
	docker image ls gitw3 --format '{{.CreatedAt}}|{{.Tag}}' | sort -r | cut -d'|' -f2 | head -n "$keep_builds"
} | sort -u)

kept() {
	local tag
	for tag in "${keep[@]}"; do
		# Checkouts are named after a commit prefix.
		[[ "$tag" == "$1"* ]] && return 0
	done
	return 1
}

for repo in gitw3 gitw3-platform-manifest-sync; do
	for tag in $(docker image ls "$repo" --format '{{.Tag}}'); do
		if ! kept "$tag"; then
			run docker image rm "$repo:$tag" >/dev/null && echo "removed image $repo:$tag"
		fi
	done
done

for checkout in source-*; do
	[ -d "$checkout" ] || continue
	# A build in progress has a fresh checkout but no image yet.
	[ -n "$(find "$checkout" -maxdepth 0 -mmin -180)" ] && continue
	if ! kept "${checkout#source-}"; then
		run rm -rf -- "$checkout" && echo "removed $checkout"
	fi
done

# Config backups are small; keep the newest few of each kind.
for old in $(ls -1t compose.yaml.pre-* 2>/dev/null | tail -n +"$((keep_backups + 1))"); do
	run rm -f -- "$old" && echo "removed $old"
done
if [ -d config-backups ]; then
	for old in $(ls -1t config-backups/* 2>/dev/null | tail -n +"$((keep_backups * 3 + 1))"); do
		run rm -f -- "$old" && echo "removed $old"
	done
fi

run docker image prune --force >/dev/null
run docker builder prune --all --force --filter "until=$cache_age" >/dev/null
echo "kept builds: ${keep[*]}"
