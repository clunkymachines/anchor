VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
VERSION_LDFLAGS = -X anchor/internal/buildinfo.Version=$(VERSION)

.PHONY: build run test version

build:
	mkdir -p build
	go build -trimpath -ldflags "$(VERSION_LDFLAGS)" -o build/anchor .
	go build -trimpath -ldflags "$(VERSION_LDFLAGS)" -o build/coap-frontend ./cmd/coap-frontend
	go build -trimpath -ldflags "$(VERSION_LDFLAGS)" -o build/fleet-sim ./cmd/fleet-sim

run:
	go run -ldflags "$(VERSION_LDFLAGS)" .

test:
	go test ./...

version:
	@echo $(VERSION)
