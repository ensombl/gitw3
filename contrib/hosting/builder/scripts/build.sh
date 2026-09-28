#!/usr/bin/env bash
# Builds every image of a GitW3 managed hosting job, pushes them by digest,
# scans them and reports the result to GitW3 with an HMAC-signed callback.
#
# Runs inside a fresh job container on the untrusted build node. Dockerfile
# RUN steps execute inside BuildKit, not in this container, so they never see
# the push token or the callback key.
set -euo pipefail

: "${JOB_ID:?}" "${SPEC:?}" "${SOURCE_URL:?}" "${CALLBACK_URL:?}"
: "${REGISTRY_USER:?}" "${REGISTRY_PUSH_TOKEN:?}" "${CALLBACK_HMAC_KEY:?}"
BUILDKIT_HOST="${BUILDKIT_HOST:-unix:///run/buildkit/buildkitd.sock}"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
spec="$(printf '%s' "$SPEC" | base64 -d)"
nonce="$(jq -r '.nonce' <<<"$spec")"
registry="$(jq -r '.registry' <<<"$spec")"
severity="$(jq -r '.scan.severity // "CRITICAL"' <<<"$spec")"
digests='{}'
scan='{"critical":0,"high":0,"medium":0,"low":0}'

callback() {
	local status="$1" error="${2:-}"
	local body signature
	body="$(jq -cn \
		--argjson job_id "$JOB_ID" --arg nonce "$nonce" --arg status "$status" \
		--argjson digests "$digests" --argjson scan "$scan" --arg error "$error" \
		--arg runner "${FORGEJO_RUNNER_NAME:-$(hostname)}" \
		'{job_id: $job_id, nonce: $nonce, status: $status, digests: $digests, scan: $scan, error: $error, runner: $runner}')"
	signature="$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$CALLBACK_HMAC_KEY" -hex | sed 's/^.*= //')"
	curl -fsS --retry 5 --retry-all-errors --max-time 30 \
		-H 'Content-Type: application/json' \
		-H "X-GitW3-Signature: sha256=$signature" \
		--data-binary "$body" "$CALLBACK_URL" >/dev/null
}

fail() {
	echo "::error::$1" >&2
	callback failure "$1" || echo "could not report the failure to GitW3" >&2
	exit 1
}

# inside_source resolves a spec path and refuses anything outside the checkout.
inside_source() {
	local resolved
	resolved="$(realpath -m "$workdir/src/$1")"
	case "$resolved" in
	"$workdir/src" | "$workdir/src/"*) printf '%s' "$resolved" ;;
	*) fail "path $1 escapes the repository" ;;
	esac
}

echo "::group::Fetch source"
mkdir -p "$workdir/src"
curl -fsS --max-time 300 --max-filesize 1073741824 "$SOURCE_URL" -o "$workdir/source.tar.gz" ||
	fail "could not download the release source"
tar -xzf "$workdir/source.tar.gz" -C "$workdir/src" --no-same-owner --no-same-permissions ||
	fail "could not unpack the release source"
echo "::endgroup::"

export DOCKER_CONFIG="$workdir/docker"
mkdir -p "$DOCKER_CONFIG"
printf '%s' "$REGISTRY_PUSH_TOKEN" | docker login "$registry" -u "$REGISTRY_USER" --password-stdin >/dev/null ||
	fail "registry login failed"
docker buildx create --name "gitw3-$JOB_ID" --driver remote "$BUILDKIT_HOST" >/dev/null ||
	fail "BuildKit is not reachable"
trap 'docker buildx rm "gitw3-$JOB_ID" >/dev/null 2>&1 || true; rm -rf "$workdir"' EXIT

count() {
	jq --arg s "$1" '[.Results[]?.Vulnerabilities[]? | select(.Severity == $s)] | length' "$2"
}

while read -r image; do
	service="$(jq -r '.service' <<<"$image")"
	repository="$(jq -r '.repository' <<<"$image")"
	context="$(inside_source "$(jq -r '.context' <<<"$image")")"
	dockerfile="$(inside_source "$(jq -r '.dockerfile' <<<"$image")")"
	[ -f "$dockerfile" ] || fail "Dockerfile $(jq -r '.dockerfile' <<<"$image") not found"
	ref="$registry/$repository"

	echo "::group::Build ${service:-image}"
	docker buildx build --builder "gitw3-$JOB_ID" \
		--file "$dockerfile" \
		--tag "$ref:job-$JOB_ID" \
		--provenance=false --sbom=false \
		--cache-from "type=registry,ref=$ref:buildcache" \
		--cache-to "type=registry,ref=$ref:buildcache,mode=max,ignore-error=true" \
		--metadata-file "$workdir/meta.json" \
		--push "$context" || fail "docker build failed for ${service:-the image}"
	digest="$(jq -r '."containerimage.digest"' "$workdir/meta.json")"
	[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || fail "BuildKit reported no image digest"
	digests="$(jq -c --arg s "$service" --arg d "$digest" '. + {($s): $d}' <<<"$digests")"
	echo "::endgroup::"

	echo "::group::Scan ${service:-image}"
	TRIVY_USERNAME="$REGISTRY_USER" TRIVY_PASSWORD="$REGISTRY_PUSH_TOKEN" \
		trivy image --quiet --format json --output "$workdir/trivy.json" \
		--scanners vuln --severity LOW,MEDIUM,HIGH,CRITICAL "$ref@$digest" ||
		fail "vulnerability scan failed"
	scan="$(jq -c \
		--argjson c "$(count CRITICAL "$workdir/trivy.json")" --argjson h "$(count HIGH "$workdir/trivy.json")" \
		--argjson m "$(count MEDIUM "$workdir/trivy.json")" --argjson l "$(count LOW "$workdir/trivy.json")" \
		'{critical: (.critical + $c), high: (.high + $h), medium: (.medium + $m), low: (.low + $l)}' <<<"$scan")"
	trivy convert --format table --severity "$severity,CRITICAL" "$workdir/trivy.json" || true
	echo "::endgroup::"
done < <(jq -c '.images[]' <<<"$spec")

scan="$(jq -c --arg summary "trivy severity threshold $severity" '. + {summary: $summary}' <<<"$scan")"
callback success
echo "Reported $(jq -r 'length' <<<"$digests") image(s) to GitW3."
