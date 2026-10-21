FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=1 go build -ldflags="-X needle/internal/server.Version=${VERSION} -X main.BuildCommit=${COMMIT}" -o /needle-server ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 -h /data -s /sbin/nologin needle && \
    mkdir -p /data && chown needle:needle /data && chmod 700 /data
COPY --from=builder /needle-server /usr/local/bin/
USER needle
WORKDIR /data
EXPOSE 8008
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8008/api/health || exit 1
ENTRYPOINT ["needle-server"]
ENV NEEDLE_LISTEN=:8008
CMD ["-db", "/data/needle.db"]
