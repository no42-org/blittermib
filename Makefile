.PHONY: all build test verify run tidy fmt vet lint clean help check-tools hooks prepare-assets

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
	rm -f $(BIN)
	rm -rf dist/

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
