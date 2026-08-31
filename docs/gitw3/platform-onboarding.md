# Platform onboarding and manifest publication

GitW3's **New** action creates W3DS platforms. The guided path commits
`.w3ds/platform.json`; the separately deployed `platform-manifest-sync` process treats that file on
the default branch as the source of truth for the platform's eName and Marketplace profile.

User authentication is a separate concern. GitW3 renders its own **Continue with W3DS** login button
and stable `/user/login/w3ds` route. That entry point delegates to the active authentication source
named `W3DS`, backed by the MetaState `w3ds-oidc-bridge`; it is not supplied by the manifest publisher.

Normal API-created repositories, migrations, forks, templates, and push-created repositories retain
their upstream Forgejo behavior. Repositories without the manifest are ignored by the publisher.

## Manifest contract

Version 1 contains:

```json
{
  "schemaVersion": 1,
  "platformName": "my-platform",
  "displayName": "My Platform",
  "description": "What the platform helps people do",
  "version": "0.1.0",
  "ename": null,
  "url": "https://my-platform.example",
  "logoUrl": "https://my-platform.example/logo.png",
  "domains": ["productivity", "work"],
  "inSubmission": false,
  "submissionVersion": "",
  "isDraft": true
}
```

The creation form derives the repository slug and initial `platformName` from `displayName`, adding a
numeric suffix when the owner already has that repository name. `platformName` and the provisioned
`ename` are immutable. `ename` is initially `null`; the publisher
provisions the platform eVault keylessly and commits the assigned value with its bot account. Platform
publication does not wait for a deployment and does not require an application key. Changes merged to
the default branch update the same public User-profile MetaEnvelope. Deleting the manifest or repository
archives the profile so the Marketplace hides it.

GitW3 loads the selectable `domains` from the published ontology at
`https://ontology.w3ds.metastate.foundation/domains`. Multiple domains may be selected. They are
published as both `domains` and `requestedDomains` in the platform profile so each release-specific PPA
application carries the exact requested domain set. Legacy manifests containing only `category` remain
readable, but must select ontology domains before applying for PPA.

`version` is controlled by the latest stable Forgejo release tag rather than an editable form field.
Tags such as `v1.2.3` are normalized to `1.2.3`. PPA state is scoped by `submissionVersion`; every newer
release clears the previous submission so the new version must be reviewed and submitted explicitly.

Each PPA application must be approved by a repository or organization owner/admin using the eID wallet
connected to their GitW3 W3DS login. GitW3 creates a 15-minute, single-use `w3ds://sign` request, resolves
the signer's eVault through the production Registry, validates the Registry key-binding certificate, and
verifies the P-256 wallet signature. Only then does it commit `inSubmission: true`. The same manifest
contains `submissionProof`: the exact release statement, domains, repository commit, signer eName,
signature, public key, Registry certificate, and verification time. The publisher stores that proof with
the PlatformProfile in the platform's own eVault, so the review record is bound to the submitted release.

The separate Deploy tab can create an ECDSA P-256 key for a deployment attestation. Only the public
SPKI key reaches GitW3. The PKCS#8 private key is delivered in a one-time JSON download and is never
stored by GitW3 or the publisher. This deployment key is never used to create or control the platform
identity.

## Build and run the publisher

Build locally with:

```sh
make platform-manifest-sync
```

Or build `docker/platform-manifest-sync/Dockerfile`. Mount the state directory on durable storage and
configure:

| Environment variable | Purpose |
| --- | --- |
| `PLATFORM_SYNC_LISTEN_ADDR` | HTTP listen address; defaults to `:8090`. |
| `PLATFORM_SYNC_STATE_PATH` | Durable BoltDB file; defaults to `data/platform-manifest-sync.db`. |
| `PLATFORM_SYNC_FORGEJO_URL` | Public or internal GitW3 base URL. |
| `PLATFORM_SYNC_FORGEJO_TOKEN` | Dedicated bot token with read/write repository access. |
| `PLATFORM_SYNC_WEBHOOK_SECRET` | HMAC secret shared with the Forgejo system webhook. |
| `PLATFORM_SYNC_INTERNAL_TOKEN` | Secret used by GitW3 to read publication status. |
| `PLATFORM_SYNC_REGISTRY_URL` | W3DS Registry base URL; defaults to the production Registry. |
| `PLATFORM_SYNC_REGISTRY_SHARED_SECRET` | Registry service credential required to inspect, transfer, and manage migrated PlatformProfiles. |
| `PLATFORM_SYNC_PROVISIONER_URL` | W3DS Provisioner base URL; defaults to the production Provisioner. |
| `PLATFORM_SYNC_VERIFICATION_ID` | Approved production provisioning verification identifier. |
| `PLATFORM_SYNC_PUBLISHER_URL` | Certified platform URL used to request eVault tokens. |

The bot token must be able to read private platform repositories and update their manifest after
provisioning. Do not reuse a human administrator token.

The publisher uses `https://registry.w3ds.metastate.foundation` and
`https://provisioner.w3ds.metastate.foundation` by default. A local GitW3 instance therefore writes
real W3DS identity and profile data without running the W3DS core stack locally. Only set the Registry
or Provisioner overrides when deliberately targeting another W3DS environment.

For example, run GitW3 locally against production W3DS with:

```sh
PLATFORM_SYNC_FORGEJO_URL=http://localhost:3000 \
PLATFORM_SYNC_FORGEJO_TOKEN='<service-token>' \
PLATFORM_SYNC_WEBHOOK_SECRET='<webhook-secret>' \
PLATFORM_SYNC_INTERNAL_TOKEN='<internal-token>' \
PLATFORM_SYNC_REGISTRY_SHARED_SECRET='<registry-service-secret>' \
PLATFORM_SYNC_VERIFICATION_ID='<approved-production-verification-id>' \
PLATFORM_SYNC_PUBLISHER_URL=http://localhost:8090 \
go run ./cmd/platform-manifest-sync
```

## Port an existing application

The **Port an existing app** choice creates an empty GitW3 repository first. It does not import code or
create an initial commit. The resulting handoff page provides the destination's HTTPS/SSH remote,
minimal manual Git commands, and a copyable prompt for the coding agent already working in the
application.

The handoff is a server-enforced three-step flow:

1. **Push application.** Only the coding-agent prompt, destination remote, and optional manual Git
   commands are shown while the repository is empty. The prompt tells the agent to preserve the existing
   history and remote, install the W3DS skill, inspect any existing `.w3ds` state, integrate a new manifest
   only when the application has no W3DS identity, and make GitW3 the new `origin` before pushing. Existing
   eNames must be preserved rather than claimed through new-platform onboarding.
2. **Migrate eName.** GitW3 unlocks the eName and token form only after a pushed commit is present. It sends
   the raw token to the publisher for one-time inspection, stores only its fingerprint, checks that the
   connected W3DS wallet is an author of the source profile, and asks that wallet to sign a statement bound
   to the already-created repository. Once verified, GitW3 commits the staged migration proof into
   `.w3ds/platform.json`; it preserves a compatible manifest already pushed by the application. A
   conflicting eName or invalid manifest stops the migration instead of being replaced.
3. **Activate cutover.** Only a staged migration unlocks the activation link. The public listing remains
   under its existing management until an administrator reviews the staged result, publishes the required
   stable release, enters the original token again, and explicitly activates the cutover from the
   repository's W3DS page.

The migration endpoint also rejects attempts to start step 2 while the repository is empty. A browser
submission that misses the JavaScript signing client redirects back to the handoff with an error instead
of exposing an API response or processing the token.

The handoff stays available at `/<owner>/<repository>/onboarding/port`, including after the application
has been pushed, so the signed eName migration can happen immediately or later. The ordinary Forgejo
server-side importer at `/repo/migrate` remains available for advanced imports, but it is not the guided
GitW3 application-porting flow.

## Register the system webhook

Create exactly one active system webhook targeting:

```text
https://<publisher>/webhooks/forgejo
```

Enable `repository`, `push`, and `release` events, set the shared secret, and set
`config.is_system_webhook` to the literal string `"true"`. A system hook covers repositories created
before and after registration. The publisher verifies `X-Forgejo-Signature`, ignores non-default
branches, and safely coalesces repeat delivery.

Then enable repository-page status in GitW3's `app.ini`:

```ini
[platform_manifest_sync]
ENABLED = true
URL = http://platform-manifest-sync:8090
INTERNAL_TOKEN = <same value as PLATFORM_SYNC_INTERNAL_TOKEN>
ONTOLOGY_URL = https://ontology.w3ds.metastate.foundation
REGISTRY_URL = https://registry.w3ds.metastate.foundation
TIMEOUT = 2s
SIGNATURE_TIMEOUT = 10s
```

The equivalent container variables are `GITEA__platform_manifest_sync__ENABLED`, `URL`,
`INTERNAL_TOKEN`, `ONTOLOGY_URL`, `REGISTRY_URL`, `TIMEOUT`, and `SIGNATURE_TIMEOUT`.

## Failure behavior

Repository creation and pushes never depend synchronously on W3DS availability. The publisher stores
pending work before returning `202`, retries transient failures with bounded exponential backoff, and
shows the latest state on the repository page. Invalid manifests and attempts to change the bound slug
or eName remain failed until a valid default-branch commit arrives.
