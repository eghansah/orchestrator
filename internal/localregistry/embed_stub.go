//go:build !with_ingressd_image

package localregistry

// IngressdTar is nil when the orchestrator was built without --tags with_ingressd_image.
// The registry will start but serve no images until the tarball is baked in.
var IngressdTar []byte
