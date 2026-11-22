# All Go tooling runs inside the golang container: nothing is installed on the host.
GO_IMAGE ?= golang:1.26-bookworm
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

DOCKER_GO = docker run --rm -u $(shell id -u):$(shell id -g) \
	-e HOME=/tmp -e GOCACHE=/src/.cache/go-build -e GOMODCACHE=/src/.cache/gomod \
	-v $(CURDIR):/src -w /src $(1) $(GO_IMAGE)

.PHONY: tidy fmt vet test agents images e2e web clean

tidy:   ## resolve dependencies, write go.sum
	$(call DOCKER_GO) go mod tidy

fmt:
	$(call DOCKER_GO) gofmt -w cmd internal test

vet:
	$(call DOCKER_GO) go vet ./...

test:   ## unit + PostgreSQL integration tests (throwaway postgres container)
	scripts/test.sh

agents: ## cross-compile agent binaries into dist/
	$(call DOCKER_GO,-e CGO_ENABLED=0 -e GOOS=linux   -e GOARCH=amd64) go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tunnelhub-agent-linux-amd64 ./cmd/agent
	$(call DOCKER_GO,-e CGO_ENABLED=0 -e GOOS=linux   -e GOARCH=arm64) go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tunnelhub-agent-linux-arm64 ./cmd/agent
	$(call DOCKER_GO,-e CGO_ENABLED=0 -e GOOS=windows -e GOARCH=amd64) go build -trimpath -ldflags "$(LDFLAGS)" -o dist/tunnelhub-agent-windows-amd64.exe ./cmd/agent

images: ## build the control-plane image
	docker build --target control --build-arg VERSION=$(VERSION) -t tunnelhub/control:$(VERSION) .

e2e:    ## full relay+agent test on an isolated docker network
	test/e2e/run.sh

web:    ## build the React dashboard into internal/web/static (node runs in Docker)
	docker run --rm -u $(shell id -u):$(shell id -g) -e HOME=/tmp -e npm_config_cache=/tmp/npm \
		-v $(CURDIR):/src -w /src/web node:22-alpine sh -c "npm ci --no-audit --no-fund && npm run build"

clean:
	rm -rf dist .cache
