VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS   := -s -w -extldflags '-static' -X main.version=$(VERSION)
DIST      := dist
PLATFORMS := linux/amd64 linux/arm64

ORCHESTRATOR_SRCS    := $(shell find cmd/orchestrator internal pkg -name '*.go') go.mod go.sum
CTL_SRCS             := $(shell find cmd/ctl internal pkg -name '*.go') go.mod go.sum
INGRESSD_SRCS        := $(shell find cmd/ingressd internal pkg -name '*.go') go.mod go.sum
PROXYD_SRCS          := $(shell find cmd/proxyd internal pkg -name '*.go') go.mod go.sum
MESHROUTERD_SRCS     := $(shell find cmd/meshrouterd internal pkg -name '*.go') go.mod go.sum
INGRESSD_IMAGE       := internal/localregistry/images/ingressd.tar
MESHROUTERD_IMAGE    := internal/localregistry/images/meshrouterd.tar
# Backwards-compat alias used by release targets
REGISTRY_IMAGE       := $(INGRESSD_IMAGE)

# Web UI: vite writes to internal/webui/dist, which is embedded into the
# orchestrator binary. index.html is the rule's stamp file.
WEB_SRCS   := $(shell find web/src -type f) \
              web/index.html web/package.json web/package-lock.json \
              web/tsconfig.json web/vite.config.ts
WEBUI_DIST := internal/webui/dist/index.html

# ── Air-gapped install vendoring ────────────────────────────────────────────
# nerdctl-full bundles containerd, runc, CNI plugins, rootlesskit,
# slirp4netns, bypass4netns, tini, and containerd-rootless-setuptool.sh —
# everything install.sh needs to set up rootless nerdctl on a target machine
# with no internet access. Fetched here (on a machine that HAS internet) and
# checked against a checksum pinned in mk/nerdctl-checksums.mk, not one
# fetched over the same channel as the artifact.
NERDCTL_VERSION     := 2.3.4
NERDCTL_RELEASE_URL := https://github.com/containerd/nerdctl/releases/download/v$(NERDCTL_VERSION)
NERDCTL_FULL_AMD64  := nerdctl-full-$(NERDCTL_VERSION)-linux-amd64.tar.gz
NERDCTL_FULL_ARM64  := nerdctl-full-$(NERDCTL_VERSION)-linux-arm64.tar.gz
VENDOR_DIR          := $(DIST)/vendor
include mk/nerdctl-checksums.mk

.PHONY: all build web registry-image meshrouterd-image release vendor-nerdctl offline-release clean

# ── Local build ───────────────────────────────────────────────────────────────

all: build

build: $(DIST)/orchestrator $(DIST)/ctl $(DIST)/ingressd $(DIST)/proxyd $(DIST)/meshrouterd $(DIST)/orchestrator-full

$(DIST)/orchestrator: $(ORCHESTRATOR_SRCS) $(WEBUI_DIST)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/orchestrator

$(DIST)/orchestrator-full: $(ORCHESTRATOR_SRCS) $(WEBUI_DIST) $(INGRESSD_IMAGE) $(MESHROUTERD_IMAGE)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build \
	  -tags with_ingressd_image,with_meshrouterd_image \
	  -ldflags "$(LDFLAGS)" -o $@ ./cmd/orchestrator

$(DIST)/ctl: $(CTL_SRCS)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/ctl

$(DIST)/ingressd: $(INGRESSD_SRCS)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/ingressd

$(DIST)/proxyd: $(PROXYD_SRCS)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/proxyd

$(DIST)/meshrouterd: $(MESHROUTERD_SRCS)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/meshrouterd

# ── Web UI ────────────────────────────────────────────────────────────────────

web: $(WEBUI_DIST)

$(WEBUI_DIST): $(WEB_SRCS)
	cd web && npm run build

# ── Registry images ───────────────────────────────────────────────────────────

# ingressd image: uses haproxy base (requires Docker or the pure-Go build-image tool)
$(INGRESSD_IMAGE): $(INGRESSD_SRCS)
	docker build -t ingressd:latest -f cmd/ingressd/Dockerfile .
	docker save ingressd:latest -o $(INGRESSD_IMAGE)
	@echo "ingressd image saved to $(INGRESSD_IMAGE)"

registry-image: $(INGRESSD_IMAGE)

# meshrouterd image: scratch base, built with the pure-Go build-image tool
$(MESHROUTERD_IMAGE): $(MESHROUTERD_SRCS)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux go build -ldflags "$(LDFLAGS)" -o $(DIST)/meshrouterd ./cmd/meshrouterd
	go run ./cmd/build-image \
	  --binary $(DIST)/meshrouterd \
	  --base scratch \
	  --binary-dest /meshrouterd \
	  --entrypoint /meshrouterd \
	  --dirs data/mesh,etc \
	  --files etc/passwd,etc/group \
	  --tag meshrouterd:$(VERSION) \
	  --output $(MESHROUTERD_IMAGE)
	@echo "meshrouterd image saved to $(MESHROUTERD_IMAGE)"

meshrouterd-image: $(MESHROUTERD_IMAGE)

# ── Release ───────────────────────────────────────────────────────────────────
# Produces dist/orchestrator-<version>-<os>-<arch>.tar.gz for each platform
# plus dist/checksums.sha256.

release: $(DIST)/checksums.sha256

$(DIST)/checksums.sha256: $(foreach p,$(PLATFORMS),$(DIST)/orchestrator-$(VERSION)-$(subst /,-,$(p)).tar.gz)
	cd $(DIST) && sha256sum *.tar.gz > checksums.sha256
	@echo ""
	@echo "Release artifacts in $(DIST)/:"
	@ls -lh $(DIST)/
	@echo ""
	@cat $(DIST)/checksums.sha256

$(DIST)/orchestrator-$(VERSION)-linux-amd64.tar.gz: $(ORCHESTRATOR_SRCS) $(CTL_SRCS) $(PROXYD_SRCS) $(WEBUI_DIST) $(INGRESSD_IMAGE) $(MESHROUTERD_IMAGE) scripts/install.sh
	@mkdir -p $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -tags with_ingressd_image,with_meshrouterd_image -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/orchestrator ./cmd/orchestrator
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/ctl ./cmd/ctl
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/proxyd ./cmd/proxyd
	cp README.md docs/deploy.md docs/production.md scripts/install.sh \
		$(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/
	chmod +x $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/install.sh
	tar -czf $@ -C $(DIST)/tmp orchestrator-$(VERSION)-linux-amd64
	rm -rf $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64

$(DIST)/orchestrator-$(VERSION)-linux-arm64.tar.gz: $(ORCHESTRATOR_SRCS) $(CTL_SRCS) $(PROXYD_SRCS) $(WEBUI_DIST) scripts/install.sh
	@mkdir -p $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64
	docker build --platform linux/arm64 -t ingressd:linux-arm64 -f cmd/ingressd/Dockerfile .
	docker save ingressd:linux-arm64 -o $(INGRESSD_IMAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	  go build -ldflags "$(LDFLAGS)" \
	  -o $(DIST)/meshrouterd-arm64 ./cmd/meshrouterd
	go run ./cmd/build-image \
	  --binary $(DIST)/meshrouterd-arm64 --base scratch \
	  --binary-dest /meshrouterd --entrypoint /meshrouterd \
	  --dirs data/mesh,etc --files etc/passwd,etc/group \
	  --tag meshrouterd:$(VERSION) \
	  --platform linux/arm64 --output $(MESHROUTERD_IMAGE)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	  go build -tags with_ingressd_image,with_meshrouterd_image -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/orchestrator ./cmd/orchestrator
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/ctl ./cmd/ctl
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/proxyd ./cmd/proxyd
	cp README.md docs/deploy.md docs/production.md scripts/install.sh \
		$(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/
	chmod +x $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/install.sh
	tar -czf $@ -C $(DIST)/tmp orchestrator-$(VERSION)-linux-arm64
	rm -rf $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64

# ── Air-gapped offline release ──────────────────────────────────────────────
# Produces dist/orchestrator-offline-<version>-<os>-<arch>.tar.gz: the same
# contents as the regular release tarball, plus a vendored nerdctl-full
# bundle so install.sh can set up rootless containerd/nerdctl on a target
# machine with zero network access. See docs/install.md.

$(VENDOR_DIR)/$(NERDCTL_FULL_AMD64):
	@mkdir -p $(VENDOR_DIR)
	curl -fL --retry 3 -o $@.tmp $(NERDCTL_RELEASE_URL)/$(NERDCTL_FULL_AMD64)
	echo "$(NERDCTL_SHA256_AMD64)  $@.tmp" | sha256sum -c -
	mv $@.tmp $@

$(VENDOR_DIR)/$(NERDCTL_FULL_ARM64):
	@mkdir -p $(VENDOR_DIR)
	curl -fL --retry 3 -o $@.tmp $(NERDCTL_RELEASE_URL)/$(NERDCTL_FULL_ARM64)
	echo "$(NERDCTL_SHA256_ARM64)  $@.tmp" | sha256sum -c -
	mv $@.tmp $@

vendor-nerdctl: $(VENDOR_DIR)/$(NERDCTL_FULL_AMD64) $(VENDOR_DIR)/$(NERDCTL_FULL_ARM64)

offline-release: $(DIST)/checksums-offline.sha256

$(DIST)/checksums-offline.sha256: \
    $(DIST)/orchestrator-offline-$(VERSION)-linux-amd64.tar.gz \
    $(DIST)/orchestrator-offline-$(VERSION)-linux-arm64.tar.gz
	cd $(DIST) && sha256sum orchestrator-offline-*.tar.gz > checksums-offline.sha256
	@echo ""
	@echo "Offline release artifacts in $(DIST)/:"
	@ls -lh $(DIST)/orchestrator-offline-*.tar.gz
	@echo ""
	@cat $(DIST)/checksums-offline.sha256

$(DIST)/orchestrator-offline-$(VERSION)-linux-amd64.tar.gz: \
    $(DIST)/orchestrator-$(VERSION)-linux-amd64.tar.gz \
    $(VENDOR_DIR)/$(NERDCTL_FULL_AMD64) docs/install.md
	@rm -rf $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-amd64
	@mkdir -p $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-amd64
	tar -xzf $(DIST)/orchestrator-$(VERSION)-linux-amd64.tar.gz \
		-C $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-amd64 --strip-components=1
	cp $(VENDOR_DIR)/$(NERDCTL_FULL_AMD64) \
		$(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-amd64/nerdctl-full-linux-amd64.tar.gz
	cp docs/install.md $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-amd64/
	tar -czf $@ -C $(DIST)/tmp orchestrator-offline-$(VERSION)-linux-amd64
	rm -rf $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-amd64

$(DIST)/orchestrator-offline-$(VERSION)-linux-arm64.tar.gz: \
    $(DIST)/orchestrator-$(VERSION)-linux-arm64.tar.gz \
    $(VENDOR_DIR)/$(NERDCTL_FULL_ARM64) docs/install.md
	@rm -rf $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-arm64
	@mkdir -p $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-arm64
	tar -xzf $(DIST)/orchestrator-$(VERSION)-linux-arm64.tar.gz \
		-C $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-arm64 --strip-components=1
	cp $(VENDOR_DIR)/$(NERDCTL_FULL_ARM64) \
		$(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-arm64/nerdctl-full-linux-arm64.tar.gz
	cp docs/install.md $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-arm64/
	tar -czf $@ -C $(DIST)/tmp orchestrator-offline-$(VERSION)-linux-arm64
	rm -rf $(DIST)/tmp/orchestrator-offline-$(VERSION)-linux-arm64

# ── Housekeeping ──────────────────────────────────────────────────────────────
# `clean` wipes dist/ including the vendored nerdctl-full cache, so the next
# offline-release re-downloads it (~150-250MB/arch). That's intentional —
# clean is for a full reset, not routine iteration.

clean:
	rm -rf $(DIST)/
