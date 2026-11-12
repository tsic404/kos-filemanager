# Local entry points for the file manager. CI runs `lint`, `test` and
# `acceptance`, one step per target; `package` builds the installable artifact.
#
# Nothing here pushes or publishes: the repository is the delivery.
SHELL := /bin/sh
GO ?= go
JS ?= node
ENVTEST_K8S_VERSION ?= 1.37.0
ENVTEST_ASSETS ?= $(CURDIR)/bin/envtest
# The release publishes one sha512 sidecar per archive; hosts without coreutils
# can pass SHA512SUM="shasum -a 512".
SHA512SUM ?= sha512sum
PLATFORM := $(shell $(GO) env GOOS)-$(shell $(GO) env GOARCH)
ENVTEST_ARCHIVE := envtest-v$(ENVTEST_K8S_VERSION)-$(PLATFORM).tar.gz
ENVTEST_URL := https://github.com/kubernetes-sigs/controller-tools/releases/download/envtest-v$(ENVTEST_K8S_VERSION)/$(ENVTEST_ARCHIVE)
ENVTEST_STAMP := $(ENVTEST_ASSETS)/.version
# Binaries a usable asset directory must provide; the re-download decision and
# the post-unpack check read this same list, so a partial unpack stays unusable.
ENVTEST_BINARIES := kube-apiserver etcd

.PHONY: all fmt-check vet js-check lint test acceptance js-test envtest-version envtest-assets package clean

all: lint test

fmt-check:
	@unformatted=$$(gofmt -l $$(git ls-files '*.go')); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt required for:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	$(GO) vet ./...

# js-check parses the window module before a browser ever loads it: the asset is
# shipped as it is, so a syntax error would otherwise surface only at runtime.
js-check:
	$(JS) --check pkg/product/assets/app.js

# js-test runs the window's browser-side cases against the module the package
# ships; no build step and no install are involved.
js-test:
	$(JS) --test tests/acceptance/*/*.test.js

lint: fmt-check vet js-check

# test runs the unit and acceptance suites; both need a real API server.
test: envtest-assets js-test
	KUBEBUILDER_ASSETS="$(ENVTEST_ASSETS)" $(GO) test -p 1 ./...

acceptance: envtest-assets
	KUBEBUILDER_ASSETS="$(ENVTEST_ASSETS)" $(GO) test -p 1 ./tests/... -count=1 -v

# package writes the SDK-packed artifact and the digest an install request names
# (KOSD-C-17, SDK-C-4).
package:
	$(GO) run ./cmd/kos-filemanager-pack -out dist

envtest-version:
	@echo $(ENVTEST_K8S_VERSION)

envtest-assets:
	@set -e; \
	has_binaries() { \
		for b in $(ENVTEST_BINARIES); do [ -x "$(ENVTEST_ASSETS)/$$b" ] || return 1; done; \
	}; \
	if [ "$$(cat "$(ENVTEST_STAMP)" 2>/dev/null)" = "$(ENVTEST_K8S_VERSION)" ] && has_binaries; then \
		exit 0; \
	fi; \
	echo "downloading $(ENVTEST_URL)"; \
	tmp=$$(mktemp -d); \
	trap 'rm -rf "$$tmp"' EXIT; \
	curl -fsSL "$(ENVTEST_URL)" -o "$$tmp/$(ENVTEST_ARCHIVE)"; \
	curl -fsSL "$(ENVTEST_URL).sha512" -o "$$tmp/$(ENVTEST_ARCHIVE).sha512"; \
	want=$$(cut -d' ' -f1 "$$tmp/$(ENVTEST_ARCHIVE).sha512"); \
	got=$$($(SHA512SUM) "$$tmp/$(ENVTEST_ARCHIVE)" | cut -d' ' -f1); \
	if [ -z "$$want" ] || [ "$$want" != "$$got" ]; then \
		echo "sha512 mismatch for $(ENVTEST_ARCHIVE)" >&2; \
		exit 1; \
	fi; \
	rm -rf "$(ENVTEST_ASSETS)"; \
	mkdir -p "$(ENVTEST_ASSETS)"; \
	tar -xzf "$$tmp/$(ENVTEST_ARCHIVE)" -C "$(ENVTEST_ASSETS)" --strip-components=2 "controller-tools/envtest"; \
	if ! has_binaries; then \
		echo "envtest assets incomplete: $(ENVTEST_BINARIES) must be executable under $(ENVTEST_ASSETS)" >&2; \
		exit 1; \
	fi; \
	printf '%s\n' "$(ENVTEST_K8S_VERSION)" > "$(ENVTEST_STAMP)"

clean:
	rm -rf bin dist
