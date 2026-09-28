# platform/builder

The central build pipeline for GitW3 managed hosting. GitW3 seeds this
directory into the `platform/builder` repository during bootstrap
(`contrib/hosting/bootstrap.sh`). Only instance admins can write to that
repository; app repositories can only trigger it through GitW3.

## How a build runs

1. GitW3 dispatches `.forgejo/workflows/build.yml` with a job id, a base64
   build spec, a signed source URL and the callback URL.
2. The `gitw3-builder` runner, registered **only on this repository** and
   running on the firewalled build node, starts a fresh job container from
   `image/Dockerfile`.
3. `scripts/build.sh` downloads the release source from the signed,
   expiring URL, builds each image with the node's rootless BuildKit, pushes
   it to `REGISTRY_HOST/deployments/<image>` and scans it with Trivy.
4. It POSTs `{job_id, nonce, status, digests, scan}` to GitW3 with an
   `X-GitW3-Signature: sha256=<hmac>` header. GitW3 verifies the HMAC, the
   single-use nonce and that each digest exists under the expected image name
   before anything is deployed.

## Repository settings

| Kind | Name | Value |
| --- | --- | --- |
| Variable | `REGISTRY_USER` | `deployments-push` |
| Variable | `BUILDKIT_HOST` | `unix:///run/buildkit/buildkitd.sock` (default) |
| Secret | `REGISTRY_PUSH_TOKEN` | token of `deployments-push` with `write:package` |
| Secret | `CALLBACK_HMAC_KEY` | same value as `[hosting] CALLBACK_SECRET` in app.ini |

The push token belongs to a bot that is only a member of the `deployments`
organization, so it can write images there and nowhere else.
