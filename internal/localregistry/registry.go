// Package localregistry provides a read-only in-memory OCI registry backed by
// go-containerregistry. On startup the embedded ingressd Docker-save tarball is
// pushed into the registry so nerdctl can pull it without a network connection.
package localregistry

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/google/go-containerregistry/pkg/name"
	gcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// Registry wraps the go-containerregistry in-memory OCI registry.
type Registry struct {
	handler  http.Handler
	count    int
	imageTag string // repo:tag the image is served under, e.g. "ingressd:v1.4.0"
	digest   string // manifest digest of the loaded image, e.g. "sha256:…"
}

// New creates an in-memory OCI registry. If tarData is non-nil, the first image
// in the Docker-save tarball is pushed into the registry under imageTag (a
// "repo:tag" reference; defaults to "ingressd:latest" when empty) before
// returning. The image's manifest digest is recorded and exposed via Digest.
func New(tarData []byte, imageTag string) (*Registry, error) {
	if imageTag == "" {
		imageTag = "ingressd:latest"
	}
	h := gcrregistry.New(gcrregistry.WithBlobHandler(gcrregistry.NewInMemoryBlobHandler()))
	r := &Registry{handler: h, imageTag: imageTag}
	if len(tarData) > 0 {
		if err := r.loadTar(tarData); err != nil {
			return nil, err
		}
		r.count = 1
	}
	return r, nil
}

// Handler returns the HTTP handler implementing the Registry V2 API.
func (r *Registry) Handler() http.Handler { return r.handler }

// ImageCount returns the number of images loaded into the registry.
func (r *Registry) ImageCount() int { return r.count }

// ImageTag returns the "repo:tag" the loaded image is served under.
func (r *Registry) ImageTag() string { return r.imageTag }

// Digest returns the manifest digest of the loaded image ("sha256:…"), or ""
// if no image was loaded. It is content-addressed: identical image bytes yield
// the same digest, so callers can detect content changes across builds.
func (r *Registry) Digest() string { return r.digest }

// loadTar starts a temporary server on a random loopback port, pushes the first
// image from tarData into the in-memory registry over HTTP, then shuts the
// temporary server down. The blobs and manifest remain in the in-memory store.
func (r *Registry) loadTar(tarData []byte) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("temp listen: %w", err)
	}
	srv := &http.Server{Handler: r.handler}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	ref, err := name.NewTag(ln.Addr().String()+"/"+r.imageTag, name.Insecure)
	if err != nil {
		return fmt.Errorf("parse ref: %w", err)
	}

	img, err := tarball.Image(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(tarData)), nil
	}, nil)
	if err != nil {
		return fmt.Errorf("load tarball: %w", err)
	}

	dig, err := img.Digest()
	if err != nil {
		return fmt.Errorf("compute digest: %w", err)
	}
	r.digest = dig.String()

	if err := remote.Write(ref, img, remote.WithTransport(http.DefaultTransport)); err != nil {
		return fmt.Errorf("push image: %w", err)
	}
	return nil
}
