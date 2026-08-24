FROM golang:1.26.6-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/goi ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin goi \
    && mkdir -p /data /backups \
    && chown goi:goi /data /backups
COPY --from=build /out/goi /usr/local/bin/goi
COPY LICENSE /usr/share/licenses/goi/LICENSE
COPY NOTICE /usr/share/licenses/goi/NOTICE

ENV APP_LISTEN=:8080 \
    APP_DATA_DIR=/data \
    APP_DATABASE_PATH=/data/vocab.sqlite

VOLUME ["/data"]
EXPOSE 8080
USER goi
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/goi"]
