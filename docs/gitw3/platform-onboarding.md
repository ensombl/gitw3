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
  "category": "Productivity",
  "publicKey": "z..."
}
```

`platformName` and the provisioned `ename` are immutable. `ename` is initially `null`; the publisher
provisions the platform eVault and commits the assigned value with its bot account. Changes merged to
the default branch update the same public User-profile MetaEnvelope. Deleting the manifest or repository
archives the profile so the Marketplace hides it.

The generated-key option creates an ECDSA P-256 key in the browser. Only the public SPKI key reaches
GitW3. The PKCS#8 private key is delivered in a one-time JSON download and is never stored by GitW3 or
the publisher.

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
PLATFORM_SYNC_VERIFICATION_ID='<approved-production-verification-id>' \
PLATFORM_SYNC_PUBLISHER_URL=http://localhost:8090 \
go run ./cmd/platform-manifest-sync
```

## Register the system webhook

Create exactly one active system webhook targeting:

```text
https://<publisher>/webhooks/forgejo
```

Enable `repository` and `push` events, set the shared secret, and set
`config.is_system_webhook` to the literal string `"true"`. A system hook covers repositories created
before and after registration. The publisher verifies `X-Forgejo-Signature`, ignores non-default
branches, and safely coalesces repeat delivery.

Then enable repository-page status in GitW3's `app.ini`:

```ini
[platform_manifest_sync]
ENABLED = true
URL = http://platform-manifest-sync:8090
INTERNAL_TOKEN = <same value as PLATFORM_SYNC_INTERNAL_TOKEN>
TIMEOUT = 2s
```

The equivalent container variables are `GITEA__platform_manifest_sync__ENABLED`, `URL`,
`INTERNAL_TOKEN`, and `TIMEOUT`.

## Failure behavior

Repository creation and pushes never depend synchronously on W3DS availability. The publisher stores
pending work before returning `202`, retries transient failures with bounded exponential backoff, and
shows the latest state on the repository page. Invalid manifests and attempts to change the bound slug
or eName remain failed until a valid default-branch commit arrives.
