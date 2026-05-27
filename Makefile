VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS   := -s -w -X main.version=$(VERSION)
DIST      := dist
PLATFORMS := linux/amd64 linux/arm64

ORCHESTRATOR_SRCS := $(shell find cmd/orchestrator internal pkg -name '*.go') go.mod go.sum
CTL_SRCS          := $(shell find cmd/ctl internal pkg -name '*.go') go.mod go.sum

.PHONY: all build release clean

# ── Local build ───────────────────────────────────────────────────────────────

all: build

build: bin/orchestrator bin/ctl

bin/orchestrator: $(ORCHESTRATOR_SRCS)
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/orchestrator

bin/ctl: $(CTL_SRCS)
	go build -ldflags "$(LDFLAGS)" -o $@ ./cmd/ctl

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

define build-release
GOOS=$(word 1,$(subst /, ,$(1))) GOARCH=$(word 2,$(subst /, ,$(1)))
endef

$(DIST)/orchestrator-$(VERSION)-linux-amd64.tar.gz: $(ORCHESTRATOR_SRCS) $(CTL_SRCS)
	@mkdir -p $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/orchestrator ./cmd/orchestrator
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/ctl ./cmd/ctl
	cp README.md docs/deploy.md docs/production.md \
		$(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64/
	tar -czf $@ -C $(DIST)/tmp orchestrator-$(VERSION)-linux-amd64
	rm -rf $(DIST)/tmp/orchestrator-$(VERSION)-linux-amd64

$(DIST)/orchestrator-$(VERSION)-linux-arm64.tar.gz: $(ORCHESTRATOR_SRCS) $(CTL_SRCS)
	@mkdir -p $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/orchestrator ./cmd/orchestrator
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" \
		-o $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/ctl ./cmd/ctl
	cp README.md docs/deploy.md docs/production.md \
		$(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64/
	tar -czf $@ -C $(DIST)/tmp orchestrator-$(VERSION)-linux-arm64
	rm -rf $(DIST)/tmp/orchestrator-$(VERSION)-linux-arm64

# ── Housekeeping ──────────────────────────────────────────────────────────────

clean:
	rm -rf bin/ $(DIST)/
