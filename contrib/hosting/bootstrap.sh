#!/usr/bin/env bash
# One-time setup of GitW3 managed hosting on a running GitW3 instance.
#
# Creates the deployments and platform organizations, the registry bot users
# and their scoped tokens, seeds platform/builder, configures its secrets and
# (optionally) registers the registry in Dokploy. Prints the app.ini section
# and the runner registration token for the build node.
#
# Usage:
#   GITW3_URL=https://git.example.com GITW3_ADMIN_TOKEN=... \
#   [DOKPLOY_URL=http://10.10.0.3:3000 DOKPLOY_API_KEY=...] \
#   contrib/hosting/bootstrap.sh
set -euo pipefail

: "${GITW3_URL:?set GITW3_URL}" "${GITW3_ADMIN_TOKEN:?set GITW3_ADMIN_TOKEN (admin, all scopes)}"
GITW3_URL="${GITW3_URL%/}"
here="$(cd "$(dirname "$0")" && pwd)"
registry_host="${REGISTRY_HOST:-$(printf '%s' "$GITW3_URL" | sed -E 's#^https?://##; s#/.*##')}"

api() {
	local method="$1" path="$2" body="${3:-}"
	local args=(-fsS -X "$method" -H "Authorization: token $GITW3_ADMIN_TOKEN" -H 'Content-Type: application/json')
	[ -n "$body" ] && args+=(--data "$body")
	curl "${args[@]}" "$GITW3_URL/api/v1$path"
}

exists() {
	curl -fsS -o /dev/null -H "Authorization: token $GITW3_ADMIN_TOKEN" "$GITW3_URL/api/v1$1"
}

random() {
	head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

ensure_org() {
	exists "/orgs/$1" || api POST /orgs "$(jq -cn --arg n "$1" --arg d "$2" '{username: $n, full_name: $d, visibility: "private"}')" >/dev/null
	echo "org $1 ready"
}

ensure_bot() {
	local name="$1" password="$2"
	if ! exists "/users/$name"; then
		api POST /admin/users "$(jq -cn --arg n "$name" --arg p "$password" \
			'{username: $n, email: ($n + "@noreply.gitw3.local"), password: $p, must_change_password: false, visibility: "private"}')" >/dev/null
	else
		api PATCH "/admin/users/$name" "$(jq -cn --arg n "$name" --arg p "$password" '{login_name: $n, source_id: 0, password: $p, must_change_password: false}')" >/dev/null
	fi
}

bot_token() {
	local name="$1" password="$2" scope="$3"
	curl -fsS -u "$name:$password" -H 'Content-Type: application/json' \
		--data "$(jq -cn --arg n "gitw3-hosting-$(date +%s)" --arg s "$scope" '{name: $n, scopes: [$s]}')" \
		"$GITW3_URL/api/v1/users/$name/tokens" | jq -r '.sha1'
}

ensure_team() {
	local org="$1" team="$2" permission="$3" member="$4"
	local id
	id="$(api GET "/orgs/$org/teams/search?q=$team" | jq -r --arg t "$team" '.data[] | select(.name == $t) | .id' | head -n1)"
	if [ -z "$id" ]; then
		id="$(api POST "/orgs/$org/teams" "$(jq -cn --arg t "$team" --arg p "$permission" \
			'{name: $t, includes_all_repositories: true, units: ["repo.packages"], units_map: {"repo.packages": $p}, permission: $p}')" | jq -r '.id')"
	fi
	api PUT "/teams/$id/members/$member" >/dev/null
}

echo "== Organizations"
ensure_org deployments "GitW3 managed hosting images"
ensure_org platform "GitW3 platform services"

echo "== Registry bots"
push_password="$(random)"
pull_password="$(random)"
ensure_bot deployments-push "$push_password"
ensure_bot deployments-pull "$pull_password"
ensure_team deployments publishers write deployments-push
ensure_team deployments readers read deployments-pull
push_token="$(bot_token deployments-push "$push_password" write:package)"
pull_token="$(bot_token deployments-pull "$pull_password" read:package)"

echo "== platform/builder"
exists /repos/platform/builder || api POST /orgs/platform/repos '{"name": "builder", "private": true, "default_branch": "main", "description": "GitW3 managed hosting build pipeline"}' >/dev/null
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
git clone -q "https://gitw3:$GITW3_ADMIN_TOKEN@${GITW3_URL#https://}/platform/builder.git" "$workdir/builder" 2>/dev/null || git init -q -b main "$workdir/builder"
cp -R "$here/builder/." "$workdir/builder/"
git -C "$workdir/builder" add -A
if ! git -C "$workdir/builder" diff --cached --quiet; then
	git -C "$workdir/builder" -c user.name=gitw3-bootstrap -c user.email=bootstrap@gitw3.local commit -qm "Update GitW3 builder"
	git -C "$workdir/builder" push -q "https://gitw3:$GITW3_ADMIN_TOKEN@${GITW3_URL#https://}/platform/builder.git" HEAD:main
fi

callback_secret="${CALLBACK_SECRET:-$(random)}"
api PUT /repos/platform/builder/actions/secrets/REGISTRY_PUSH_TOKEN "$(jq -cn --arg d "$push_token" '{data: $d}')" >/dev/null
api PUT /repos/platform/builder/actions/secrets/CALLBACK_HMAC_KEY "$(jq -cn --arg d "$callback_secret" '{data: $d}')" >/dev/null
api POST /repos/platform/builder/actions/variables/REGISTRY_USER '{"value": "deployments-push"}' >/dev/null ||
	api PUT /repos/platform/builder/actions/variables/REGISTRY_USER '{"value": "deployments-push"}' >/dev/null
runner_token="$(api GET /repos/platform/builder/actions/runners/registration-token | jq -r '.token')"

registry_id=""
if [ -n "${DOKPLOY_URL:-}" ] && [ -n "${DOKPLOY_API_KEY:-}" ]; then
	echo "== Dokploy registry"
	registry_id="$(curl -fsS -X POST -H "x-api-key: $DOKPLOY_API_KEY" -H 'Content-Type: application/json' \
		--data "$(jq -cn --arg u "$registry_host" --arg p "$pull_token" \
			'{registryName: "gitw3", username: "deployments-pull", password: $p, registryUrl: $u, registryType: "cloud", imagePrefix: ""}')" \
		"${DOKPLOY_URL%/}/api/registry.create" | jq -r '.registryId // empty')"
fi

cat <<EOF

== Done. Add this to app.ini and restart GitW3:

[hosting]
ENABLED = true
DOKPLOY_URL = ${DOKPLOY_URL:-http://<manager-private-ip>:3000}
DOKPLOY_API_KEY = <Dokploy API key>
DOKPLOY_ENVIRONMENT_ID = <environment of the "gitw3-apps" Dokploy project>
DOKPLOY_REGISTRY_ID = ${registry_id:-<registry id; create it in Dokploy with user deployments-pull and the token below>}
SWARM_PROXY_URL = http://<manager-private-ip>:2375
REGISTRY_HOST = $registry_host
CALLBACK_SECRET = $callback_secret

[hosting.domains]
BASE_DOMAIN = <apps domain>
PROVIDER = wildcard
TARGET_IP = <terraform output ingress_ip>

== Build node: set builder_runner_token in terraform.tfvars to
$runner_token
EOF
if [ -z "$registry_id" ]; then
	echo
	echo "== Registry pull token for Dokploy (user deployments-pull):"
	echo "$pull_token"
fi
