FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=1 go build -ldflags="-X needle/internal/server.Version=${VERSION}" -o /needle-server ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -h /data -s /sbin/nologin needle
COPY --from=builder /needle-server /usr/local/bin/
USER needle
WORKDIR /data
EXPOSE 8008
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8008/api/health || exit 1
ENTRYPOINT ["needle-server"]
CMD ["-l", ":8008", "-db", "/data/needle.db"]
