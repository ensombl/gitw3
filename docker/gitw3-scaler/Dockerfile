# GitW3 swarm worker autoscaler. Build from the repository root:
#   docker build -f docker/gitw3-scaler/Dockerfile -t gitw3-scaler .
FROM docker.io/library/golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o /out/gitw3-scaler ./cmd/gitw3-scaler

FROM docker.io/library/debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home /data scaler \
 && mkdir -p /data && chown scaler /data
COPY --from=build /out/gitw3-scaler /usr/local/bin/gitw3-scaler
USER scaler
VOLUME /data
EXPOSE 9180
HEALTHCHECK --interval=30s --timeout=5s CMD ["/usr/local/bin/gitw3-scaler", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/gitw3-scaler"]
