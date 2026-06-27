FROM golang:1.26-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=1 go build -o /needle-server ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /needle-server /usr/local/bin/
EXPOSE 8008
VOLUME /data
ENTRYPOINT ["needle-server"]
