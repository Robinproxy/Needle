.PHONY: build-agent build-server build clean release

build-agent:
	go build -o bin/needle-agent ./cmd/agent

build-server:
	go build -o bin/needle-server ./cmd/server

build: build-agent build-server

release:
	rm -rf release/
	mkdir -p release/
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o release/needle-server-linux-amd64 ./cmd/server
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc go build -o release/needle-server-linux-arm64 ./cmd/server
	GOOS=linux GOARCH=amd64 go build -o release/needle-agent-linux-amd64 ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o release/needle-agent-linux-arm64 ./cmd/agent
	cp scripts/install-server.sh release/
	cp scripts/install-agent.sh release/
	cp agent.yaml.example release/
	cd release && \
	  tar czf needle-linux-amd64.tar.gz \
	    needle-server-linux-amd64 needle-agent-linux-amd64 \
	    install-server.sh install-agent.sh agent.yaml.example && \
	  tar czf needle-linux-arm64.tar.gz \
	    needle-server-linux-arm64 needle-agent-linux-arm64 \
	    install-server.sh install-agent.sh agent.yaml.example && \
	  sha256sum needle-*.tar.gz > checksums.txt

clean:
	rm -rf bin/ data/ release/

run-server:
	./bin/needle-server -l :8008 -token your-token

run-agent:
	./bin/needle-agent agent.yaml

dev-server:
	go run ./cmd/server -l :8008 -token your-token
