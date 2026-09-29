# Managed hosting (one-click deploy)

GitW3 can build a tagged release of a repository and run it on an autoscaling Docker Swarm on
DigitalOcean, managed through Dokploy. The Deploy tab has two sub-tabs:

- **Managed hosting**: pick a release, click **Deploy**, get a URL such as
  `https://admiring-lovelace.apps.example.com`.
- **Self-hosted**: the existing W3DS deployment wizard for apps you run yourself.

Users in **Simple Mode** (the default) only see their repositories, the Deploy tab (Managed only) and
releases. They can switch to Advanced Mode from the user menu or *Settings → Appearance*. The instance
default is `[ui] DEFAULT_SIMPLE_MODE` (default `true`).

## How a deploy works

1. The user deploys a stable semver release (`v1.2.3`). Only releases deploy. A target can opt into
   **Deploy new releases automatically**, and into **Deploy every push to <default branch>**: each push
   to the default branch is published as the next patch release (`v1.2.3` → `v1.2.4`), which then
   deploys. Pushes that only change `.w3ds/` (GitW3's own manifest sync) and commits that already have
   a release are skipped. In W3DS platform repositories a pushed version tag is published as a release
   automatically, so it can be certified.
2. GitW3 reads `.forgejo/deploy.yml` at the release commit. If the file is missing but a `Dockerfile`
   exists, it deploys one `web` target on the Dockerfile's first `EXPOSE`d port (default 3000).
3. The first deploy of a target creates its Dokploy app and claims a ready domain from the pool. It
   also generates the target's W3DS deployment key and asks the deployer's eID wallet to sign the W3DS
   deployment record **once**. Later releases are published to W3DS by signing with that deployment
   key (`POST /api/v1/deployments/{id}/versions` on the publisher), so no further wallet taps are needed.
   Only that first deploy needs a PPA-certified release. Later versions inherit the certification the
   deployment was created with and go live without another review, unless the PPA explicitly denies a
   version, which fails its deploy.
4. GitW3 dispatches the central `platform/builder` workflow. The untrusted build node downloads the
   source from a signed, expiring URL. It builds with BuildKit, pushes to
   `REGISTRY_HOST/deployments/<owner>-<repo>-<target>` and scans with Trivy.
5. The builder reports back with an HMAC-signed callback carrying a single-use nonce. GitW3 accepts a
   digest only if it exists in the registry under that target's image name. Critical CVEs block the
   deploy unless `[hosting.scan] POLICY = warn`.
6. GitW3 sets the environment and deploys the image **by digest** through Dokploy. Swarm rolls the
   service with `failure_action: rollback`, so a bad release never replaces a good one. GitW3 marks the
   deployment *Live* once every replica runs the new digest, and sets a `gitw3/deploy/<target>` commit
   status.

Rollback redeploys an earlier live release's digest **and** its environment snapshot without
rebuilding.

## Simple Mode deploy page and error relay

Simple Mode shows the Deploy page as three steps:

1. **Get your app ready.** A copy-paste prompt tells the user's AI assistant how to add a Dockerfile
   that GitW3 can run: listen on `0.0.0.0:$PORT`, a `.dockerignore`, no secrets, and a version tag.
2. **Publish a version.** A pushed `vX.Y.Z` tag is published as a release and deploys like one.
3. **Pick an address and deploy.**

Once an app is live, the page shows its URL, a "Deploy latest version" button, a rename form and the
AI prompt.

Failures are explained where the user will see them:

- **Preflight, before anything is queued.** Deploy stops at once when the Dockerfile has no `FROM` or
  copies a `.env` file into the image. It warns when the start command uses `localhost`, when no port
  is declared, or when `COPY .` runs without a `.dockerignore`.
- **Build errors.** The builder sends back the last meaningful lines of the BuildKit log, such as the
  failing `RUN` step and its compiler or package-manager error.
- **Crashes after deploy.** When a rollout is rolled back or fails, GitW3 reads the failed Swarm task's
  error and the app's last 30 log lines through the socket proxy.

Each failure is shown with a "fix-it" prompt that contains the exact error.

## `.forgejo/deploy.yml`

```yaml
version: 1
targets:
  - name: web
    kind: dockerfile
    dockerfile: Dockerfile
    context: .
    port: 3000
    replicas: 2
    domain: www.example.com      # optional custom domain, verified through DNS
    healthcheck:
      path: /healthz
      interval: 10s
      timeout: 3s
    resources:
      cpu: "0.5"
      memory: 512M
    auto_deploy: true            # seeds the Deploy-tab switch for a new target

  - name: api-stack
    kind: compose
    compose: deploy/compose.yml
    service: api                 # compose service that receives public traffic
    port: 4000
```

Compose files are validated for swarm stack mode before anything is built. Services with `build:`
are built and pinned to digests by GitW3. The following are rejected:

- privileged or host-namespace options (`privileged`, `network_mode`, `pid`, `ipc`, `devices`, `cap_add`)
- published ports, `container_name`, `links`, `extends`
- volumes, compose secrets and configs (managed hosting is stateless; use DigitalOcean Managed
  Databases for state)

GitW3 injects these environment variables:

- `PORT`, unless the app sets its own
- `GITW3_URL`, `GITW3_RELEASE`, `GITW3_COMMIT`, `GITW3_TARGET`
- `W3DS_DEPLOYMENT_ENAME`
- `W3DS_DEPLOYMENT_KEY_JSON`, the app's `w3ds-deployment-key.json` content (set on every service; file mounts cannot reach worker nodes)

The deployment key is fully managed: GitW3 generates it on the first deploy, stores it encrypted, has
the deployer's wallet authorise it once, and signs every later version with it. For compose apps, GitW3
writes the environment into every service, since Swarm stacks do not otherwise receive it.

## Domains

Every routable target gets an automatic name like `verbing-scientist.<BASE_DOMAIN>` from a reserve
pool. The `hosting_domain_pool` cron keeps `POOL_SIZE` names ready so a first deploy never waits for
DNS.

- `PROVIDER = wildcard` (default): `*.BASE_DOMAIN` already points at the ingress; names cost nothing.
- `PROVIDER = cloudflare`: an `A` record is created per name through the Cloudflare API
  (`CLOUDFLARE_API_TOKEN` needs `Zone.DNS:Edit` on `CLOUDFLARE_ZONE_ID`).

Users can also type the address they want, e.g. `myshop` → `myshop.<BASE_DOMAIN>`. The Deploy page
suggests one based on the repository name and checks availability live. Names must be 3–40 lowercase
letters, digits and dashes, and platform names like `infra`, `www`, `api` and `admin` are reserved. With
the wildcard provider a new name needs no DNS change; renaming releases the old name.

Users can attach their own domain. The UI shows the record to create: a `CNAME` to the app's pool name,
or an `A` record to `TARGET_IP`. **Check DNS** verifies it before routing, and Traefik issues the
certificate through Let's Encrypt.

## Infrastructure (P0)

Everything lives in `contrib/hosting/`:

| Path | What |
| --- | --- |
| `infra/terraform/` | VPC, the `forgejo`, `manager`, `builder` and seed `worker` droplets, tag firewalls, reserved IP, Managed Postgres, Spaces backup bucket, optional Cloudflare wildcard |
| `infra/cloud-init/` | Node bootstrap. The worker template is also rendered by the scaler |
| `bootstrap.sh` | Creates the `deployments`/`platform` orgs, registry bots and scoped tokens, seeds `platform/builder` and its secrets, and registers the registry in Dokploy. Prints the `app.ini` section |
| `builder/` | Contents of the `platform/builder` repository: workflow, build script and job image |
| `manager/` | Swarm stacks for the read-only status proxy and the scaler |
| `backup/` | Nightly Spaces backups (`ROLE=forgejo` or `ROLE=manager`) |

Bring-up order:

1. `terraform apply` with `builder_runner_token` and `swarm_worker_join_token` empty.
2. Install GitW3 on `forgejo-01` (see `production-deployment-agent-prompt.md`), with its data in
   `/srv/gitw3` and the database from `terraform output postgres`.
3. Open Dokploy on the manager (VPN only, port 3000). Create an API key, a project `gitw3-apps` and note
   its environment ID.
4. Run `GITW3_URL=… GITW3_ADMIN_TOKEN=… DOKPLOY_URL=… contrib/hosting/bootstrap.sh`.
5. Put the printed runner token and `docker swarm join-token -q worker` into `terraform.tfvars`, then
   apply again. This registers the build runner and creates the seed worker.
6. On the manager:
   - `docker stack deploy -c contrib/hosting/manager/socket-proxy.stack.yml gitw3-status`
   - `docker swarm update --task-history-limit 2`
   - `docker stack deploy -c contrib/hosting/manager/janitor.stack.yml gitw3-janitor`, which prunes old
     app images and stopped containers on every node, including workers added later.
7. Add the `[hosting]` section below to `app.ini` and restart GitW3.

Exit check: a manual `docker push` from the build node and a pull on a worker both work. `curl` from the
build node to the manager's or a worker's private IP times out.

### `app.ini`

```ini
[ui]
DEFAULT_SIMPLE_MODE = true

[hosting]
ENABLED = true
DOKPLOY_URL = http://10.10.0.3:3000
DOKPLOY_API_KEY = …
DOKPLOY_ENVIRONMENT_ID = …
SWARM_PROXY_URL = http://10.10.0.3:2375
PLACEMENT_CONSTRAINTS = node.role==worker
BUILDER_REPO = platform/builder
BUILDER_WORKFLOW = build.yml
REGISTRY_OWNER = deployments
REGISTRY_HOST = git.example.com
REGISTRY_PULL_USER = deployments-pull
REGISTRY_PULL_TOKEN = …             ; read:package token printed by bootstrap.sh
CALLBACK_SECRET = …                 ; same as the builder's CALLBACK_HMAC_KEY secret
REQUIRE_W3DS = true
KEEP_DIGESTS = 10
HEALTH_TIMEOUT = 10m
BUILD_QUEUE_ALERT = 15m
SCALER_METRICS_URL = http://10.10.0.3:9180/metrics
REGISTRY_ALERT_BYTES = 107374182400

[hosting.scan]
POLICY = block                      ; or warn
SEVERITY = CRITICAL

[hosting.domains]
BASE_DOMAIN = apps.example.com
PROVIDER = wildcard                 ; or cloudflare
POOL_SIZE = 10
TARGET_IP = 203.0.113.10
ALLOW_CUSTOM = true
```

The Dokploy procedure names used by `services/hosting/dokploy.go` are:

- `application.create/update/saveEnvironment/saveDockerProvider/deploy/one/delete`
- `compose.create/update/deploy/one/delete`
- `domain.create/delete`
- `mounts.create`

Dokploy changes quickly; re-check these against your version before upgrading.

## Autoscaling (P4)

`cmd/gitw3-scaler` runs as the `gitw3-scaler` stack on the primary manager. It reaches Docker through
its own socket proxy (nodes, services, tasks, swarm) and DigitalOcean with a **custom-scoped token**
(droplet create/delete, read tags/VPC).

- **Scale up** when tasks are pending (the swarm has no room), when reserved CPU/memory is above
  `CPU_HIGH`, or when there are fewer than `MIN_WORKERS` workers. Each new droplet gets a fresh join
  token.
- **Scale down** when utilization stays below `CPU_LOW` for `LOW_SUSTAIN` and there are more than
  `MIN_WORKERS` workers. The scaler picks the worker with the fewest tasks (newest first on ties), drains
  it, deletes the droplet once it is empty, and runs `docker node rm` once the node is down.
- Every tick reconciles against `ListByTag(worker)`. Droplets from a crashed provision are adopted or
  destroyed, and workers that stay down are replaced.
- State lives in SQLite (`STATE_PATH`), and the worker join token is rotated every `TOKEN_ROTATION`
  (7 days).
- `:9180/metrics` exposes the worker count, pending tasks, utilization and `gitw3_scaler_at_max`, which
  GitW3 turns into admin notices.

Until the scaler runs, add workers by hand with `infra/cloud-init/worker.yaml.tftpl`.

## Hardening and operations (P5)

- **Scan gate:** the builder reports Trivy counts, and GitW3 blocks or warns according to
  `[hosting.scan]`.
- **Token rotation:**
  - The swarm worker join token is rotated automatically.
  - Rotate the registry bot tokens by re-running `bootstrap.sh`. It issues new tokens and updates the
    builder secret; then put the new pull token in `app.ini` `REGISTRY_PULL_TOKEN`.
  - Rotate `CALLBACK_SECRET` by setting `CALLBACK_SECRET=<new>` for `bootstrap.sh` and updating
    `app.ini` together.
- **Three managers:**
  1. Set `manager_count = 3`.
  2. On the primary, run `docker swarm join-token manager`, then join the two new droplets with that
     token.
  3. Label each new manager with `docker node update --label-add gitw3.role=manager <node>`.

  Dokploy stays on the primary; if it is lost, running apps keep serving until you restore it from the
  nightly Dokploy backup onto a surviving manager.
- **Registry GC:** the weekly `hosting_registry_gc` cron keeps, for every target:
  - the last `KEEP_DIGESTS` released images
  - the live image
  - any image still in flight

  It removes other `job-*` images older than a day, and Forgejo's package cleanup then reclaims the
  blobs.
- **Deleted apps and repositories:** deleting an app removes its Dokploy app, domains, images, variables
  and deployment history. Deleting a repository does the same for each of its apps. The hourly
  `hosting_cleanup` cron catches what those paths miss, such as repositories removed together with
  their owner: it removes targets whose repository is gone, and Dokploy apps GitW3 created
  ("Managed by GitW3 for …") that no target owns. Anything younger than an hour is left alone while it
  may still be provisioning.
- **Node disks:** the `gitw3-janitor` stack prunes images older than three days that no container
  uses, plus stopped containers, on every node every six hours.
- **Alerts:** `hosting_alerts` (every 5 minutes) raises admin notices for:
  - a build waiting longer than `BUILD_QUEUE_ALERT`
  - failed or rolled-back deploys
  - the scaler at `MAX_WORKERS` or failing
  - registry size over `REGISTRY_ALERT_BYTES`
  - an unreachable Dokploy or swarm API

  `/metrics` also exports `gitw3_hosting_*` gauges when `[metrics]` is enabled.
- **Backups:** install `backup/gitw3-backup.{sh,service,timer}` on `forgejo-01` (`ROLE=forgejo`: repos,
  registry blobs and a `pg_dump`) and on the manager (`ROLE=manager`: Dokploy data and database, scaler
  SQLite). The Spaces bucket keeps versions for 30 days.

## Credential map

| Credential | Held by | Scope |
| --- | --- | --- |
| Registry push token | `platform/builder` secret `REGISTRY_PUSH_TOKEN` | `deployments-push`, member of `deployments` only |
| Registry pull token | `app.ini` `REGISTRY_PULL_TOKEN`, sent to Dokploy with each deploy | `deployments-pull`, read only |
| Source access | Signed per-job URL (`SOURCE_URL_TTL`) | one commit archive |
| Dokploy API key | `app.ini` `DOKPLOY_API_KEY` | Dokploy API |
| Callback HMAC key | `app.ini` `CALLBACK_SECRET` + builder secret `CALLBACK_HMAC_KEY` | signs build callbacks |
| Deployment keys | `hosting_target.private_key_enc` (encrypted) + mounted into the app | the app's W3DS deployment |
| DO token (scaler) | swarm secret `gitw3_scaler_do_token` | droplet create/delete, read tags/VPC |
| Swarm join token | fetched per provision | worker join, rotated weekly |

## Failure behavior

| Failure | What happens |
| --- | --- |
| Build node down | Deployments stay *Queued*; an alert fires after `BUILD_QUEUE_ALERT`; running apps are unaffected |
| Build or scan fails | *Build failed*, log on the Deploy tab, commit status red |
| Bad release | Swarm rolls back automatically; the deployment is marked *Rolled back* |
| Manager down | Apps keep serving; no deploys or scaling until it returns |
| Worker dies | Swarm reschedules; the scaler replaces the droplet |
| GitW3 down | No deploys and no image pulls for new tasks; running apps are fine |
