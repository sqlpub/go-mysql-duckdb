.PHONY: build test tidy run fmt vet clean

GO ?= go
BIN := bin/syncer

build:
	CGO_ENABLED=1 $(GO) build -o $(BIN) ./cmd/syncer

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

clean:
	rm -rf bin/
