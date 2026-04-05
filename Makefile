VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"

.PHONY: build install cross-compile test lint clean

build:
	go build $(LDFLAGS) -o bin/agentd ./cmd/agentd

install: build
	cp bin/agentd /usr/local/bin/agentd

cross-compile:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/agentd-linux-amd64 ./cmd/agentd
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/agentd-linux-arm64 ./cmd/agentd
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o dist/agentd-darwin-amd64 ./cmd/agentd
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/agentd-darwin-arm64 ./cmd/agentd
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/agentd-windows-amd64.exe ./cmd/agentd

test:
	go test ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/ dist/
