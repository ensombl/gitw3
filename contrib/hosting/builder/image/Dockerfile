# Job image for the gitw3-builder runner label: the tools build.sh needs and
# nothing else. BuildKit itself runs as a separate rootless daemon on the
# build node and is reached through BUILDKIT_HOST.
FROM docker.io/library/debian:bookworm-slim

ARG TRIVY_VERSION=0.56.2
ARG BUILDX_VERSION=0.18.0

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git jq openssl docker.io nodejs \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /usr/local/lib/docker/cli-plugins \
 && curl -fsSL -o /usr/local/lib/docker/cli-plugins/docker-buildx \
      "https://github.com/docker/buildx/releases/download/v${BUILDX_VERSION}/buildx-v${BUILDX_VERSION}.linux-amd64" \
 && chmod +x /usr/local/lib/docker/cli-plugins/docker-buildx \
 && curl -fsSL "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-64bit.tar.gz" \
      | tar -xz -C /usr/local/bin trivy \
 && useradd --create-home --uid 1000 builder

USER builder
WORKDIR /home/builder
