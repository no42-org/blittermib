.PHONY: all build test verify run tidy fmt vet lint clean help check-tools hooks prepare-assets generate fetch-standard-mibs fetch-fonts dist docker-build

# Pinned templ version — keep in sync with go.mod's github.com/a-h/templ entry.
TEMPL_VERSION := v0.3.1001

# Pinned htmx version — fetched into internal/server/assets/htmx.min.js
# by `make fetch-htmx`. The vendored copy is committed so self-hosted
# builds do not require network access.
HTMX_VERSION := 2.0.4

GO      ?= go
BIN     := blittermib
PKG     := ./...
LDFLAGS := -s -w

# External tool requirements (Go module versions are pinned in go.mod)
LIBSMI_MIN := 0.5.0

all: build

prepare-assets:
	@mkdir -p internal/server/assets
	@cp -f prototype/styles.css internal/server/assets/styles.css

generate:
	$(GO) run github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION) generate

fetch-htmx:
	@mkdir -p internal/server/assets
	curl -fL --silent --show-error -o internal/server/assets/htmx.min.js \
		https://unpkg.com/htmx.org@$(HTMX_VERSION)/dist/htmx.min.js
	@echo "fetched htmx $(HTMX_VERSION) -> internal/server/assets/htmx.min.js"

# Fetch self-hosted Inter + JetBrains Mono woff2 files from Fontsource
# (CDN-mirrored open-source fonts via jsdelivr). Vendored into
# internal/server/assets/fonts/ and embedded at build time. Both
# families are SIL OFL 1.1 licensed.
fetch-fonts:
	@mkdir -p internal/server/assets/fonts
	@for w in 400 500 600; do \
		echo ">> Inter-$$w"; \
		curl -fL --silent --show-error -o internal/server/assets/fonts/Inter-$$w.woff2 \
			https://cdn.jsdelivr.net/fontsource/fonts/inter@latest/latin-$$w-normal.woff2; \
	done
	@for w in 400 500; do \
		echo ">> JetBrainsMono-$$w"; \
		curl -fL --silent --show-error -o internal/server/assets/fonts/JetBrainsMono-$$w.woff2 \
			https://cdn.jsdelivr.net/fontsource/fonts/jetbrains-mono@latest/latin-$$w-normal.woff2; \
	done
	@echo "fetched Inter + JetBrains Mono -> internal/server/assets/fonts/"
	@echo "remove old Geist*.woff2 files manually if still present"

# Fetch IETF/IANA standard MIBs from libsmi's source distribution
# into internal/mibsbundle/bundle/. The next `go build` embeds them
# so they ship inside the binary and are usable on first run.
LIBSMI_TARBALL := https://www.ibr.cs.tu-bs.de/projects/libsmi/download/libsmi-0.5.0.tar.gz
fetch-standard-mibs:
	@mkdir -p internal/mibsbundle/bundle
	@tmp=$$(mktemp -d) && \
	curl -fL --silent --show-error -o $$tmp/libsmi.tar.gz $(LIBSMI_TARBALL) && \
	tar -xz -C $$tmp -f $$tmp/libsmi.tar.gz && \
	src=$$(find $$tmp -maxdepth 2 -type d -name mibs | head -1) && \
	cp $$src/iana/* internal/mibsbundle/bundle/ 2>/dev/null || true && \
	cp $$src/ietf/* internal/mibsbundle/bundle/ 2>/dev/null || true && \
	cp $$src/site/* internal/mibsbundle/bundle/ 2>/dev/null || true && \
	rm -rf $$tmp && \
	count=$$(ls internal/mibsbundle/bundle/ | grep -v '^README' | wc -l | tr -d ' ') && \
	echo "fetched $$count standard MIBs -> internal/mibsbundle/bundle/"

check-tools:
	@command -v smidump >/dev/null 2>&1 || { echo "smidump not found. Install libsmi >= $(LIBSMI_MIN) (brew install libsmi)"; exit 1; }
	@command -v smilint >/dev/null 2>&1 || { echo "smilint not found. Install libsmi >= $(LIBSMI_MIN) (brew install libsmi)"; exit 1; }
	@echo "libsmi tools present: $$(smidump -V 2>&1 | head -1)"

hooks:
	@command -v pre-commit >/dev/null 2>&1 || { echo "pre-commit not found. Install with: pipx install pre-commit (or pip install --user pre-commit)"; exit 1; }
	pre-commit install

build: prepare-assets
	$(GO) build -ldflags='$(LDFLAGS)' -o $(BIN) ./cmd/blittermib

test: prepare-assets
	$(GO) test -race -count=1 $(PKG)

verify: fmt-check vet test

run: build
	./$(BIN) -mibs ./mibs -data ./data

tidy:
	$(GO) mod tidy

fmt:
	gofmt -w -s .

fmt-check:
	@out=$$(gofmt -l -s .); \
	if [ -n "$$out" ]; then \
		echo "gofmt issues:"; echo "$$out"; exit 1; \
	fi

vet:
	$(GO) vet $(PKG)

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed"; exit 1; }
	golangci-lint run

clean:
	rm -f $(BIN) $(BIN).exe
	rm -rf dist/

# Cross-build release archives for every supported platform into
# dist/ plus a SHA256SUMS file. Invoked by the release CI job.
dist: prepare-assets generate
	./scripts/dist.sh

# Build the production Docker image locally. Same Dockerfile the
# release pipeline uses; tag is overridable via TAG=...
TAG ?= blittermib:dev
docker-build:
	docker build --build-arg VERSION=$$(git describe --tags --always --dirty 2>/dev/null || echo dev) \
		-t $(TAG) .

help:
	@echo "make build       compile the binary"
	@echo "make test        run tests with race detector"
	@echo "make verify      fmt-check + vet + test (CI target)"
	@echo "make run         build and run"
	@echo "make tidy        go mod tidy"
	@echo "make fmt         format code"
	@echo "make fmt-check   fail if code is not gofmt'd"
	@echo "make vet         go vet"
	@echo "make lint        golangci-lint"
	@echo "make clean       remove build artifacts"
	@echo "make check-tools verify libsmi (smidump/smilint) is installed"
	@echo "make hooks       install pre-commit git hooks"
	@echo "make generate    regenerate templ-generated _templ.go files"
	@echo "make dist        cross-build release archives into dist/"
	@echo "make docker-build build the production Docker image (TAG=...)"
	@echo "make fetch-htmx  re-vendor htmx.min.js"
	@echo "make fetch-standard-mibs  populate the embedded standard MIB bundle"
