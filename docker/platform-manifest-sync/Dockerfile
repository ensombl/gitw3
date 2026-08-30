FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/platform-manifest-sync ./cmd/platform-manifest-sync

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S gitw3 && adduser -S -G gitw3 gitw3
COPY --from=build /out/platform-manifest-sync /usr/local/bin/platform-manifest-sync
RUN mkdir -p /var/lib/platform-manifest-sync && chown gitw3:gitw3 /var/lib/platform-manifest-sync
USER gitw3
ENV PLATFORM_SYNC_LISTEN_ADDR=:8090 \
    PLATFORM_SYNC_STATE_PATH=/var/lib/platform-manifest-sync/state.db
EXPOSE 8090
ENTRYPOINT ["platform-manifest-sync"]
