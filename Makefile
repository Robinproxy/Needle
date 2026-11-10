VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS = -ldflags "-X needle/internal/server.Version=$(VERSION)"

.PHONY: build-agent build-server build clean clean-data

build-agent:
	go build $(LDFLAGS) -o bin/needle-agent ./cmd/agent

build-server:
	go build $(LDFLAGS) -o bin/needle-server ./cmd/server

build: build-agent build-server

# Release archives are built and verified by .github/workflows/release.yml.

clean:
	rm -rf bin/ release/

# Destroys the local SQLite database. Kept out of `clean` on purpose.
clean-data:
	rm -rf data/

run-server:
	./bin/needle-server -l :8008

run-agent:
	./bin/needle-agent agent.yaml

dev-server:
	go run ./cmd/server -l :8008
