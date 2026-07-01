# The Go toolchain runs in a pinned container, so nothing needs to be installed
# on the host. Caches live in-tree (gitignored) and are written as the current
# user so bind-mounted files are not left root-owned.
# Pinned to an exact Go patch + registry digest so host builds match the Dockerfile
# builders (go.mod requires go 1.26). Bump this and deploy/Dockerfile.* together.
GO_IMAGE ?= golang:1.26.4-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648
DOCKER_GO = docker run --rm -u $(shell id -u):$(shell id -g) -e HOME=/tmp -e GOCACHE=/app/.gocache -e GOMODCACHE=/app/.gomodcache -v $(CURDIR):/app -w /app $(GO_IMAGE)

.PHONY: build test vet fmt tidy ci
build: ; $(DOCKER_GO) go build ./...
test:  ; $(DOCKER_GO) go test ./...
vet:   ; $(DOCKER_GO) go vet ./...
fmt:   ; $(DOCKER_GO) gofmt -l -w .
tidy:  ; $(DOCKER_GO) go mod tidy
ci: vet test
