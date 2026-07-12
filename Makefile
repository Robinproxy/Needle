VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-X needle/internal/server.Version=$(VERSION)"

.PHONY: build-agent build-server build clean release

build-agent:
	go build $(LDFLAGS) -o bin/needle-agent ./cmd/agent

build-server:
	go build $(LDFLAGS) -o bin/needle-server ./cmd/server

build: build-agent build-server

release:
	rm -rf release/
	mkdir -p release/
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o release/needle-server-linux-amd64 ./cmd/server
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc go build $(LDFLAGS) -o release/needle-server-linux-arm64 ./cmd/server
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o release/needle-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o release/needle-agent-linux-arm64 ./cmd/agent
	cp scripts/needle-server.sh release/
	cp scripts/needle-agent.sh release/
	cp agent.yaml.example release/
	cd release && \
	  ln -f needle-server-linux-amd64 needle-server && \
	  ln -f needle-agent-linux-amd64 needle-agent && \
	  tar czf needle-linux-amd64.tar.gz \
	    needle-server needle-agent \
	    needle-server.sh needle-agent.sh agent.yaml.example && \
	  rm needle-server needle-agent && \
	  ln -f needle-server-linux-arm64 needle-server && \
	  ln -f needle-agent-linux-arm64 needle-agent && \
	  tar czf needle-linux-arm64.tar.gz \
	    needle-server needle-agent \
	    needle-server.sh needle-agent.sh agent.yaml.example && \
	  rm needle-server needle-agent && \
	  sha256sum needle-*.tar.gz > checksums.txt && \
	  for f in needle-*.tar.gz; do \
	    sha256sum "$$f" > "$$f.sha256"; \
	  done

clean:
	rm -rf bin/ data/ release/

run-server:
	./bin/needle-server -l :8008 -token "${NEEDLE_TOKEN}"

run-agent:
	./bin/needle-agent agent.yaml

dev-server:
	go run ./cmd/server -l :8008 -token "${NEEDLE_TOKEN}"