GO ?= go
VERSION := $(shell tr -d '[:space:]' < VERSION)
.PHONY: build test lint
build:
	CGO_ENABLED=1 $(GO) build -buildmode=c-shared -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o ../../dist/trae.so .
test:
	$(GO) test -race -count=1 ./...
	node --test panel.test.js
lint:
	@test -z "$$($(GO) fmt ./...)"
	$(GO) vet ./...
