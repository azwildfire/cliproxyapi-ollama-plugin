GO_IMAGE ?= golang:1.26-bookworm
VERSION ?= 0.2.1
PLUGIN_ID := cliproxyapi-ollama
PLUGIN_DIR := build/plugins/linux/amd64
PLUGIN_SO := $(PLUGIN_DIR)/$(PLUGIN_ID).so
CACHE_DIR := .cache
VERSION_LDFLAG := -X main.pluginVersion=$(VERSION)

DOCKER_GO = docker run --rm \
	--user "$$(id -u):$$(id -g)" \
	-e HOME=/src/$(CACHE_DIR)/home \
	-e GOCACHE=/src/$(CACHE_DIR)/go-build \
	-e GOMODCACHE=/src/$(CACHE_DIR)/go-mod \
	-e GOFLAGS=-buildvcs=false \
	-v "$(CURDIR):/src" \
	-w /src \
	$(GO_IMAGE)

.PHONY: test test-docker build build-local package clean

test:
	go test ./...

test-docker:
	mkdir -p $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod $(CACHE_DIR)/home
	$(DOCKER_GO) sh -ec 'go vet ./... && go test ./...'

build:
	mkdir -p $(PLUGIN_DIR) $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod $(CACHE_DIR)/home
	$(DOCKER_GO) sh -ec 'CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(PLUGIN_SO) ./cmd/$(PLUGIN_ID)'
	rm -f $(PLUGIN_DIR)/$(PLUGIN_ID).h

build-local:
	mkdir -p $(PLUGIN_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(PLUGIN_SO) ./cmd/$(PLUGIN_ID)
	rm -f $(PLUGIN_DIR)/$(PLUGIN_ID).h

package: build
	sh scripts/package-release.sh "$(VERSION)"

clean:
	rm -rf build dist $(CACHE_DIR)
