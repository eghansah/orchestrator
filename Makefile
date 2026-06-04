VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS   := -s -w -extldflags '-static' -X main.version=$(VERSION)
DIST      := dist
PLATFORMS := linux/amd64 linux/arm64

ORCHESTRATOR_SRCS := $(shell find cmd/orchestrator internal pkg -name '*.go') go.mod go.sum
CTL_SRCS          := $(shell find cmd/ctl internal pkg -name '*.go') go.mod go.sum
INGRESSD_SRCS     := $(shell find cmd/ingressd internal pkg -name '*.go') go.mod go.sum
REGISTRY_IMAGE    := internal/localregistry/images/ingressd.tar

.PHONY: all build registry-image release clean

# ── Local build ───────────────────────────────────────────────────────────────

all: build

build: $(DIST)/orchestrator $(DIST)/ctl $(DIST)/ingressd $(DIST)/orchestrator-full

$(DIST)/orchestrator: $(ORCHESTRATOR_SRCS)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/orchestrator

$(DIST)/orchestrator-full: $(ORCHESTRATOR_SRCS) $(REGISTRY_IMAGE)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -tags with_ingressd_image -ldflags "$(LDFLAGS)" -o $@ ./cmd/orchestrator

$(DIST)/ctl: $(CTL_SRCS)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/ctl

$(DIST)/ingressd: $(INGRESSD_SRCS)
	@mkdir -p $(DIST)
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/ingressd

# ── Registry image ────────────────────────────────────────────────────────────

# Builds the ingressd container image using pure Go (no Docker/nerdctl required).
# Requires internet access to pull haproxy:3.0-alpine on first run.
$(REGISTRY_IMAGE): $(DIST)/ingressd $(INGRESSD_SRCS)
	go run ./cmd/build-image \
	  --binary  $(DIST)/ingressd \
	  --output  $(REGISTRY_IMAGE) \
	  --platform linux/amd64
	@echo "Registry image saved to $(REGISTRY_IMAGE)"

registry-image: $(REGISTRY_IMAGE)

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

$(DIST)/orchestrator-$(VERSION)-linux-amd64.tar.gz: $(ORCHESTRATOR_SRCS) $(CTL_SRCS) $(REGISTRY_IMAGE)
	@mkdir -p $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags with_ingressd_image -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/orchestrator ./cmd/orchestrator
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/ctl ./cmd/ctl
	cp README.md docs/deploy.md docs/production.md \
		$(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/
	tar -czf $@ -C $(DIST)/tmp orchestrator-$(VERSION)-linux-amd64
	rm -rf $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64

$(DIST)/orchestrator-$(VERSION)-linux-arm64.tar.gz: $(ORCHESTRATOR_SRCS) $(CTL_SRCS)
	@mkdir -p $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/ingressd-arm64 ./cmd/ingressd
	go run ./cmd/build-image \
		--binary  $(DIST)/tmp/ingressd-arm64 \
		--output  $(REGISTRY_IMAGE) \
		--platform linux/arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags with_ingressd_image -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/orchestrator ./cmd/orchestrator
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/ctl ./cmd/ctl
	cp README.md docs/deploy.md docs/production.md \
		$(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/
	tar -czf $@ -C $(DIST)/tmp orchestrator-$(VERSION)-linux-arm64
	rm -rf $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64

# ── Housekeeping ──────────────────────────────────────────────────────────────

clean:
	rm -rf $(DIST)/
