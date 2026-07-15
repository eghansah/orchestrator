//go:build !with_meshrouterd_image

package localregistry

// MeshrouterdTar is nil when the orchestrator was built without --tags with_meshrouterd_image.
// The registry will start but the meshrouterd image will not be available until
// the tarball is baked in.
var MeshrouterdTar []byte
