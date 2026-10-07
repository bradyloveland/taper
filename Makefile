VERSION ?= $(shell cat internal/version/VERSION)
LDFLAGS := -s -w -X github.com/bradyloveland/taper/internal/version.override=$(VERSION) $(EXTRA_LDFLAGS)
DEV_DIR ?= $(CURDIR)/tmp/dev
# DEV_BIND=0.0.0.0 makes `make dev` reachable from other machines (for testing on a phone).
DEV_BIND ?= 127.0.0.1
DEV_PORT ?= 8088

.PHONY: build test lint check dist dev icons clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/taper ./cmd/taper

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l cmd internal docs web)" || { echo "Run gofmt on:"; gofmt -l cmd internal docs web; exit 1; }
	go vet ./...
	staticcheck ./...
	shellcheck install.sh uninstall.sh scripts/*.sh

check: lint test

# Release archives for Linux on x86-64 and ARM64, with SHA256SUMS. Set
# TAPER_SIGNING_KEY to sign them; unsigned archives install with install.sh
# but can't be installed from the Updates page.
dist:
	rm -rf dist && mkdir -p dist
	for arch in amd64 arm64; do \
		name=taper-$(VERSION)-linux-$$arch; \
		mkdir -p dist/$$name; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$$name/taper ./cmd/taper || exit 1; \
		cp install.sh uninstall.sh LICENSE README.md CHANGELOG.md dist/$$name/; \
		go run ./tools/taper-sign sign dist/$$name $(VERSION) $$arch || exit 1; \
		tar -C dist -czf dist/$$name.tar.gz $$name; \
		rm -rf dist/$$name; \
	done
	cd dist && sha256sum *.tar.gz > SHA256SUMS

# Run locally with throwaway data in tmp/dev. The setup code is printed in the log.
dev:
	mkdir -p $(DEV_DIR)
	TAPER_DATA_DIR=$(DEV_DIR) TAPER_BIND=$(DEV_BIND) TAPER_PORT=$(DEV_PORT) go run ./cmd/taper serve

# Regenerate the PNG icons from web/static/icons/icon.svg (needs Firefox and ImageMagick).
icons:
	scripts/make-icons.sh

clean:
	rm -rf bin dist tmp
