# Pinned checksums for the vendored nerdctl-full release, used by the
# offline-release Makefile targets. Update both lines by hand whenever
# NERDCTL_VERSION is bumped, copying from the release's own SHA256SUMS file:
#   https://github.com/containerd/nerdctl/releases/download/v<version>/SHA256SUMS
# Keeping the checksum in git (rather than fetching it from the same
# release page at build time) avoids trusting a value delivered over the
# same channel as the artifact it verifies.

NERDCTL_SHA256_AMD64 := 40a80a6eec6fc4f225473a87946c2098821b6ec31becef0120c8a50d7b4e432c
NERDCTL_SHA256_ARM64 := 117d65c3992e67fc4449dd501c8fba19c1d9cbf5a01fd63d68e778888f2f46e0
