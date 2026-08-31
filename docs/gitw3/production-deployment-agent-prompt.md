# GitW3 production deployment agent prompt

Copy everything under **Prompt** into the coding-agent session running on the production server. Fill in the deployment inputs first when they are known. The agent must request any missing external credential without printing it into chat, logs, shell history, or the Git repository.

## Deployment inputs

```text
GitW3 source: https://github.com/ensombl/gitw3
Git revision: main (record and deploy the exact resolved commit SHA)
GitW3 HTTPS URL: https://<GITW3_DOMAIN>
Git SSH host: <GITW3_DOMAIN>
Git SSH port: <SSH_PORT>
Administrator email: <ADMIN_EMAIL>
W3DS OIDC bridge URL/issuer: <OIDC_BRIDGE_URL>
W3DS OIDC client ID: <OIDC_CLIENT_ID>
W3DS OIDC client secret: provide through the server's secret mechanism
W3DS Registry shared secret: provide through the server's secret mechanism
Approved W3DS provisioning verification ID: <VERIFICATION_ID>
Certified publisher URL: <PUBLISHER_URL>
Object-storage endpoint/bucket/credentials: <OPTIONAL_S3_DETAILS>
SMTP details: <OPTIONAL_SMTP_DETAILS>
```

Target node:

- 4 AMD vCPU
- 16 GB RAM
- 200 GB NVMe SSD
- 8 TB monthly transfer
- No CI or Forgejo Actions runners on this node

## Prompt

```text
You are the production deployment engineer for GitW3. Deploy the application on this server completely; do not stop after producing a plan. Inspect first, implement safely, verify the result, and leave an operational runbook.

Use these supplied inputs:

- GitW3 source: https://github.com/ensombl/gitw3
- Git revision: main
- GitW3 HTTPS URL: https://<GITW3_DOMAIN>
- Git SSH host and port: <GITW3_DOMAIN>:<SSH_PORT>
- Administrator email: <ADMIN_EMAIL>
- W3DS OIDC bridge URL/issuer: <OIDC_BRIDGE_URL>
- W3DS OIDC client ID: <OIDC_CLIENT_ID>
- Approved W3DS provisioning verification ID: <VERIFICATION_ID>
- Certified publisher URL: <PUBLISHER_URL>
- Optional object storage: <OPTIONAL_S3_DETAILS>
- Optional SMTP: <OPTIONAL_SMTP_DETAILS>

The server has 4 AMD vCPU, 16 GB RAM, 200 GB NVMe, and 8 TB monthly transfer. This is sufficient for the initial production deployment. Do not install or run CI/Actions workers on this server.

Operating rules:

1. Communicate only in English. Write every progress update, command explanation, runbook, report, and final response in English, regardless of the server's locale or the language of tool output.
2. Send concise progress updates while working.
3. Read the repository documentation and relevant source before deciding configuration. Read any applicable AGENTS.md completely.
4. Inspect the OS, disks, filesystem, memory, ports, firewall, DNS, existing containers/services, existing GitW3 data, and uncommitted source changes before modifying anything.
5. Preserve existing data and source changes. Back up existing state before replacing, migrating, or reconfiguring it.
6. Do not expose or echo secrets. Never place credentials in Git, command arguments visible through process listings, shell history, build layers, generated reports, or application logs.
7. Use the server's existing secret manager when available. Otherwise use root-owned files outside the checkout with mode 0600 and mount/read them at runtime.
8. Generate independent cryptographically secure values for every database password, internal token, webhook secret, session secret, LFS secret, and application secret. Do not reuse credentials.
9. Pin images and dependencies. Never deploy a floating latest tag.
10. Resolve main to a commit SHA, record it, build that exact revision, and label/tag images with it.
11. Do not make irreversible W3DS identity changes or activate a real eName migration merely to smoke-test production.
12. If an external input such as DNS or a W3DS-issued credential is unavailable, complete every safe step that does not depend on it and report the exact missing value and next command.

Architecture that must be preserved:

- GitW3 is the customized Forgejo service built from this repository.
- platform-manifest-sync is a separate service built from docker/platform-manifest-sync/Dockerfile.
- GitW3 uses PostgreSQL in production. Do not use SQLite.
- Use Valkey or a Forgejo-compatible Redis service for cache, sessions, and queues where supported by this revision.
- GitW3 sends exactly one signed system webhook to platform-manifest-sync for repository, push, and release events.
- GitW3 calls the publisher's authenticated internal API for publication and migration status.
- platform-manifest-sync calls the production W3DS Registry and Provisioner.
- W3DS user login goes through the W3DS OIDC bridge and is independent of the publisher.
- W3DS user display names and avatars are enriched at sign-in from Awareness-as-a-Service (AaaS) using the signed-in eName. This lookup is best-effort and must not make AaaS an authentication dependency.
- platform-manifest-sync currently owns a local BoltDB state file and runs an in-process worker. Run exactly one publisher replica on a durable volume. Do not horizontally scale it until its store/queue is moved to a shared transactional backend or leader election is implemented.
- The existing-application port flow is ordered: push code, migrate and wallet-sign the existing eName using its current token, then explicitly activate public cutover.

Deployment approach:

- If this server already has a healthy supported orchestrator, integrate with it.
- Otherwise use Docker Compose (or the installed Compose-compatible runtime) in /opt/gitw3 and manage the stack with a persistent systemd unit.
- Store durable application data under /srv/gitw3 on the NVMe filesystem, separated into clearly named directories/volumes.
- Put a production reverse proxy such as Caddy or the server's established proxy in front of GitW3.
- Use private container networking for PostgreSQL, Valkey, and platform-manifest-sync.
- Expose only HTTPS and the selected Git SSH port publicly. Do not expose PostgreSQL, Valkey, publisher internal APIs, container APIs, or metrics publicly.

Perform the following work:

### 1. Preflight and deployment decision

- Record OS/version, architecture, CPU, RAM, swap, disk size/free space, filesystem, inode usage, and network interfaces.
- Record listening ports, firewall rules, container runtime/version, reverse proxies, databases, and existing Git services.
- Verify forward DNS for the GitW3 hostname. Determine whether IPv6 is intentionally supported.
- Check whether ports 80, 443, and the chosen SSH port are available or already routed correctly.
- Determine whether this is a fresh installation or upgrade. Locate and back up all existing repositories, databases, app.ini files, publisher state, secrets, and deployment manifests before changing them.
- Check Git status before switching revisions. Never discard a dirty worktree.
- State the selected deployment shape briefly, then execute it.

### 2. Source and immutable images

- Fetch https://github.com/ensombl/gitw3 safely.
- Resolve origin/main to an exact commit and record the full SHA.
- Inspect the Dockerfiles and build instructions instead of inventing build flags.
- Build the GitW3 image and the platform-manifest-sync image from that same commit.
- Tag images with the commit SHA and record their immutable image IDs/digests.
- Run relevant unit, integration, JavaScript/TypeScript, CSS, template, and locale checks available in the repository. If the complete suite is impractical on the server, run focused release-blocking tests and report exactly what ran.

### 3. Persistent services and storage

- Deploy a supported PostgreSQL release with a dedicated database, least-privilege role, SCRAM authentication, health check, durable volume, and a tested backup command.
- Deploy Valkey/Redis privately with authentication when supported by the chosen topology, a memory limit, a non-destructive eviction policy suitable for Forgejo state, a health check, and persistence if queues/sessions require it.
- Keep bare Git repositories on fast durable NVMe storage with correct ownership and permissions.
- If valid S3-compatible credentials are supplied, configure supported Forgejo object storage for LFS, attachments, packages, avatars, repository archives, Actions logs, and artifacts. Otherwise use separate durable local paths and document that 200 GB local disk is the first scaling constraint.
- Give platform-manifest-sync a dedicated durable directory for its BoltDB file. Run one replica only.
- Keep database, repository, object/file storage, publisher state, and configuration volumes independently identifiable for backup and restoration.

### 4. GitW3 production configuration

- Configure the canonical HTTPS ROOT_URL, domain, SSH domain/port, clone URLs, trusted reverse proxies, and forwarded headers correctly.
- Enable secure cookies and production security settings. Ensure external OIDC login remains compatible with the selected SameSite policy.
- Persist shared application secrets so restarts do not invalidate encrypted values, sessions, OAuth state, LFS authentication, or internal API calls.
- Disable open registration unless explicitly requested.
- Configure PostgreSQL, Valkey/Redis cache, sessions, and queues using private service names.
- Set sane database connection limits for a 4-vCPU/16-GB host rather than copying high-scale defaults.
- Configure repository, HTTP, SSH, and Git-operation timeouts for real clones and pushes without allowing unbounded requests.
- Configure SMTP only when working credentials are supplied.
- Configure structured production logs with rotation and ensure credentials, migration tokens, wallet payloads, and authorization headers are redacted.
- Configure [platform_manifest_sync] with ENABLED=true, the private publisher URL, the shared internal token, the production ontology URL, the production Registry URL, and bounded request/signature timeouts.
- Obtain a dedicated AaaS consumer key without printing it. Configure [w3ds_identity] with the production AWARENESS_URL, that AWARENESS_API_KEY through the secret mechanism, a bounded TIMEOUT, and ONLY_AUTHENTICATION=true. Never expose the key to the browser or logs. W3DS must be the only interactive web sign-in method in production.
- Disable reverse-proxy web authentication and configure OAuth2 client auto-registration so a first W3DS login can create its local GitW3 account without falling back to a password/account-linking form.
- Configure the W3DS authentication source through the supported administrative interface or CLI/API. Make this step idempotent and ensure the source name is exactly `W3DS`.

### 5. platform-manifest-sync configuration

Set and validate all required values documented by this repository:

- PLATFORM_SYNC_LISTEN_ADDR
- PLATFORM_SYNC_STATE_PATH
- PLATFORM_SYNC_FORGEJO_URL
- PLATFORM_SYNC_FORGEJO_TOKEN
- PLATFORM_SYNC_WEBHOOK_SECRET
- PLATFORM_SYNC_INTERNAL_TOKEN
- PLATFORM_SYNC_REGISTRY_URL
- PLATFORM_SYNC_REGISTRY_SHARED_SECRET
- PLATFORM_SYNC_PROVISIONER_URL
- PLATFORM_SYNC_VERIFICATION_ID
- PLATFORM_SYNC_PUBLISHER_URL

Additional requirements:

- Use a dedicated GitW3 bot account/token with only the permissions necessary to read private manifests and commit publisher updates.
- The GitW3 internal token must match PLATFORM_SYNC_INTERNAL_TOKEN.
- The Forgejo system-webhook HMAC secret must match PLATFORM_SYNC_WEBHOOK_SECRET.
- Confirm the raw legacy platform token is used only for the immediate Registry call and is never stored or logged. Only its one-way fingerprint may persist.
- Keep the publisher private unless its certified publisher URL demonstrably requires a public route. If a route is public, expose only required paths and retain authentication on internal APIs.
- Configure a restart policy, health probe, graceful shutdown, resource limit, and durable state mount.
- Keep replicas fixed at one and document why.

### 6. TLS, proxy, firewall, and SSH

- Obtain and automatically renew a trusted TLS certificate after DNS is correct.
- Redirect HTTP to HTTPS.
- Configure proxy streaming, body-size limits, buffering, and timeouts suitable for large Git smart-HTTP clone/push operations.
- Preserve client IPs only through explicitly trusted proxies.
- Configure HSTS only after HTTPS and hostname behavior are verified.
- Restrict administrative SSH access separately from the public Git SSH service.
- Enable a default-deny firewall policy that retains the current administrator's access.
- Add rate limiting carefully to login and API abuse paths without breaking Git clients, webhooks, wallet callbacks, or large pushes.

### 7. Bot account and system webhook

- Create or reuse a dedicated publisher bot account idempotently.
- Issue its scoped token without printing it. Store it through the secret mechanism.
- Create exactly one active system webhook targeting the private publisher endpoint /webhooks/forgejo when private routing is available.
- Enable repository, push, and release events.
- Set the shared HMAC secret and the literal system-webhook configuration required by GitW3.
- If rerun, update the existing matching webhook instead of creating duplicates.

### 8. Resource controls and observability

- Do not run CI/Actions jobs on this node.
- Set realistic memory/CPU limits while leaving memory for Git pack/unpack and filesystem cache. Do not allocate all 16 GB to containers.
- Add a small emergency swap file if the host has none, but do not treat swap as application capacity.
- Monitor GitW3 health, PostgreSQL, Valkey, publisher process/worker, webhook authentication failures, pending/retry work, Registry/Provisioner failures, HTTP 5xx rates, clone/push failures, TLS expiry, disk usage, inode usage, I/O latency, memory pressure, and backup freshness.
- Alert at 65% disk usage, escalate at 75%, and treat 85% as critical.
- Configure log rotation so logs cannot consume the repository disk.

### 9. Backups and restoration

- Implement encrypted scheduled backups for PostgreSQL, bare Git repositories, local/object storage, app.ini, deployment manifests, secret references/metadata, and the publisher BoltDB.
- Replicate backups off-server and preferably to another failure domain.
- Do not copy a live BoltDB file inconsistently. Use a safe database snapshot/backup method or briefly quiesce the single publisher process.
- Define retention appropriate for daily and weekly recovery points.
- Perform an actual restore drill into isolated paths or containers. Verify PostgreSQL can start/read data, a restored bare repository passes git fsck, and the publisher store opens successfully.
- Document recovery order, commands, RPO, and RTO.
- Remember that distributed Forgejo state may require a controlled maintenance window for a fully consistent backup.

### 10. Controlled rollout and rollback

- Run schema migrations as a controlled one-off operation against a fresh backup.
- Start dependencies first, then publisher, then GitW3, then proxy exposure, using health-gated dependencies where possible.
- Preserve the previous image, configuration, and database backup for rollback.
- Do not silently retry a failed destructive migration.
- Make the complete stack start automatically after a host reboot.
- Reboot or simulate a full service restart after initial validation and verify recovery.

### 11. Verification

Run and record these checks:

- HTTPS health endpoint returns success.
- TLS chain, hostname, HTTP redirect, secure cookies, and proxy headers are correct.
- Unauthenticated private/internal endpoints are rejected.
- PostgreSQL and Valkey are unreachable from the public internet.
- HTTPS and SSH clone/push work against a disposable test repository.
- GitW3 can authenticate to the publisher status API.
- An invalid Forgejo webhook signature is rejected.
- One valid signed test webhook is accepted without duplicate processing.
- W3DS login starts through the intended OIDC bridge and returns to the canonical GitW3 URL.
- Password sign-in, local registration, password recovery, OpenID, account-linking forms, and every non-W3DS OAuth source are rejected while [w3ds_identity] ONLY_AUTHENTICATION=true.
- A test W3DS login maps the newest non-platform AaaS User profile to the local display name and avatar. Confirm a simulated AaaS timeout leaves login functional and preserves the existing profile.
- New-platform and existing-application forms ask for a display name but no repository slug; the resulting collision-safe repository slug is generated server-side.
- Creating an existing-application destination creates an empty repository.
- Its handoff initially renders only Step 1; the eName/token form is absent.
- Direct Step 2 submission before a push is rejected and never renders raw JSON in the browser.
- A real test push unlocks Step 2.
- Do not use a real legacy token, provision an irreversible identity, or activate a real eName cutover as a smoke test without explicit approval.
- Restart the stack and verify health and repository access again.
- Verify the most recent backup and restore-drill evidence.

### 12. Scaling boundary

This first deployment is vertically scalable but remains a single-host failure domain. Document the path forward:

- Move PostgreSQL to a managed/replicated service before database pressure becomes material.
- Put LFS, packages, attachments, archives, logs, and artifacts in object storage.
- Introduce tested shared repository storage or an explicitly supported active/passive repository strategy before adding GitW3 web nodes.
- Ensure sessions, caches, queues, secrets, and configuration are shared before running multiple GitW3 replicas.
- Keep platform-manifest-sync at one replica until BoltDB/in-process work ownership is replaced with a shared transactional store/queue or leader election.
- Use actual CPU, I/O latency, disk growth, memory pressure, and request metrics rather than guessing when to scale.

Required final response:

Lead with whether production is live and healthy. Then provide:

1. Public URL and SSH endpoint.
2. Exact deployed Git commit.
3. Immutable image tags and digests.
4. Sanitized service topology and configuration summary.
5. Health and end-to-end verification results.
6. Persistent data and deployment-manifest locations.
7. Backup schedule and restore-test result.
8. Upgrade, rollback, restart, and disaster-recovery commands.
9. Monitoring/alert locations.
10. Remaining external blockers or risks, with exact next actions.
11. Confirmation that the publisher has exactly one replica and the system webhook has exactly one active instance.

Do not include secret values in the final response.
```

## Reference material

- [GitW3 platform onboarding and publisher architecture](./platform-onboarding.md)
- [Forgejo database preparation](https://forgejo.org/docs/latest/admin/installation/database-preparation/)
- [Forgejo configuration cheat sheet](https://forgejo.org/docs/latest/admin/config-cheat-sheet/)
- [Forgejo upgrade and backup guidance](https://forgejo.org/docs/latest/admin/upgrade/)
