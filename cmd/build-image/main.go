// build-image assembles the ingressd container image without requiring Docker
// or nerdctl on the build machine. It pulls the haproxy base image from a
// registry, appends a layer containing the ingressd binary, updates the image
// config, and saves a Docker-compatible tarball suitable for embedding via
// //go:embed.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func main() {
	var (
		binaryPath = flag.String("binary", "bin/ingressd", "path to the pre-built ingressd binary")
		baseImage  = flag.String("base", "haproxy:3.0-alpine", "base image reference")
		outputPath = flag.String("output", "internal/localregistry/images/ingressd.tar", "output tarball path")
		platform   = flag.String("platform", "linux/amd64", "target platform (os/arch)")
		imageTag   = flag.String("tag", "ingressd:latest", "image tag written into manifest.json")
	)
	flag.Parse()

	parts := strings.SplitN(*platform, "/", 2)
	if len(parts) != 2 {
		log.Fatalf("invalid platform %q — expected os/arch", *platform)
	}
	plat := v1.Platform{OS: parts[0], Architecture: parts[1]}

	// ── Pull base image ───────────────────────────────────────────────────────

	log.Printf("pulling %s (%s)...", *baseImage, *platform)
	ref, err := name.ParseReference(*baseImage)
	if err != nil {
		log.Fatalf("parse base image ref: %v", err)
	}
	base, err := remote.Image(ref,
		remote.WithPlatform(plat),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	)
	if err != nil {
		log.Fatalf("pull base image: %v", err)
	}
	logImageInfo(base)

	// ── Build ingressd layer ──────────────────────────────────────────────────

	log.Printf("reading binary %s...", *binaryPath)
	layer, err := binaryLayer(*binaryPath)
	if err != nil {
		log.Fatalf("create layer: %v", err)
	}

	// ── Assemble image ────────────────────────────────────────────────────────

	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		log.Fatalf("append layer: %v", err)
	}

	// Override entrypoint, cmd, and user from the haproxy base.
	cf, err := img.ConfigFile()
	if err != nil {
		log.Fatalf("get config: %v", err)
	}
	cf = cf.DeepCopy()
	cf.Config.Entrypoint = []string{"/usr/local/bin/ingressd"}
	cf.Config.Cmd = []string{
		"--config", "/data/ingress/config.json",
		"--haproxy-cfg", "/data/ingress/haproxy.cfg",
		"--certs-dir", "/data/ingress/certs",
	}
	cf.Config.User = "root"
	cf.Config.WorkingDir = "/"
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		log.Fatalf("set config: %v", err)
	}

	// ── Save tarball ──────────────────────────────────────────────────────────

	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	tag, err := name.NewTag(*imageTag)
	if err != nil {
		log.Fatalf("parse tag: %v", err)
	}

	// Buffer the entire tarball in memory before touching the output file.
	// tarball.Write streams layer downloads on-the-fly; if the process is
	// interrupted mid-stream the output file would be left truncated. Buffering
	// ensures we only write a complete tarball to disk.
	log.Printf("fetching layers and assembling tarball...")
	var buf bytes.Buffer
	if err := tarball.Write(tag, img, &buf); err != nil {
		log.Fatalf("assemble tarball: %v", err)
	}

	log.Printf("writing %s (%.1f MB)...", *outputPath, float64(buf.Len())/(1<<20))
	if err := os.WriteFile(*outputPath, buf.Bytes(), 0o644); err != nil {
		log.Fatalf("write file: %v", err)
	}
	log.Printf("done")
}

// binaryLayer returns a v1.Layer containing the ingressd binary at
// /usr/local/bin/ingressd and pre-created /data/ingress/certs directories.
func binaryLayer(binaryPath string) (v1.Layer, error) {
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", binaryPath, err)
	}

	opener := func() (io.ReadCloser, error) {
		var buf bytes.Buffer
		gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if err != nil {
			return nil, err
		}
		tw := tar.NewWriter(gz)

		dirs := []string{
			"usr/", "usr/local/", "usr/local/bin/",
			"data/", "data/ingress/", "data/ingress/certs/",
		}
		for _, d := range dirs {
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     d,
				Mode:     0o755,
			}); err != nil {
				return nil, fmt.Errorf("write dir header %s: %w", d, err)
			}
		}

		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     "usr/local/bin/ingressd",
			Size:     int64(len(data)),
			Mode:     0o755,
		}); err != nil {
			return nil, fmt.Errorf("write binary header: %w", err)
		}
		if _, err := tw.Write(data); err != nil {
			return nil, fmt.Errorf("write binary data: %w", err)
		}

		if err := tw.Close(); err != nil {
			return nil, err
		}
		if err := gz.Close(); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
	}

	return tarball.LayerFromOpener(opener)
}

func logImageInfo(img v1.Image) {
	size, err := img.Size()
	if err != nil {
		return
	}
	layers, err := img.Layers()
	if err != nil {
		return
	}
	log.Printf("base image: %d layers, %.1f MB compressed", len(layers), float64(size)/(1<<20))
}
