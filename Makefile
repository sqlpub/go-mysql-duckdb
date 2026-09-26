.PHONY: build test tidy run fmt vet clean release-linux release-linux-amd64 docker-image

GO ?= go
BIN := bin/syncer
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BIN) ./cmd/syncer

test:
	CGO_ENABLED=1 $(GO) test ./...

tidy:
	$(GO) mod tidy

run: build
	./$(BIN) -config configs/config.yaml

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

# Linux deployment packages (requires Docker; default linux/amd64)
release-linux:
	chmod +x scripts/build-linux.sh
	VERSION=$(VERSION) ARCHS=amd64 ./scripts/build-linux.sh

release-linux-arm64:
	chmod +x scripts/build-linux.sh
	VERSION=$(VERSION) ARCHS=arm64 ./scripts/build-linux.sh

release-linux-all:
	chmod +x scripts/build-linux.sh
	VERSION=$(VERSION) ARCHS="amd64 arm64" ./scripts/build-linux.sh

# Runnable image: docker run --rm -v $$PWD/configs:/app/configs go-mysql-duckdb:latest
docker-image:
	docker build --build-arg VERSION=$(VERSION) -t go-mysql-duckdb:$(VERSION) -t go-mysql-duckdb:latest .

clean:
	rm -rf bin/ dist/
