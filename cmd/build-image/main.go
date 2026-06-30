// build-image assembles a container image without requiring Docker or nerdctl
// on the build machine. It can pull a base image from a registry or start from
// scratch (empty base), appends a layer containing the target binary, and saves
// a Docker-compatible tarball suitable for embedding via //go:embed.
//
// Usage examples:
//
//	# ingressd (haproxy base)
//	build-image --binary dist/ingressd --base haproxy:3.0-alpine \
//	  --binary-dest /usr/local/bin/ingressd --entrypoint /usr/local/bin/ingressd \
//	  --dirs data/ingress/certs --tag ingressd:v1.0.0 \
//	  --output internal/localregistry/images/ingressd.tar
//
//	# meshrouterd (scratch base)
//	build-image --binary dist/meshrouterd --base scratch \
//	  --binary-dest /meshrouterd --entrypoint /meshrouterd \
//	  --dirs data/mesh --tag meshrouterd:v1.0.0 \
//	  --output internal/localregistry/images/meshrouterd.tar
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
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func main() {
	var (
		binaryPath  = flag.String("binary", "", "path to the pre-built binary (required)")
		binaryDest  = flag.String("binary-dest", "", "destination path inside the image (default: /usr/local/bin/<basename>)")
		baseImage   = flag.String("base", "scratch", `base image reference, or "scratch" for an empty image`)
		extraDirs   = flag.String("dirs", "", "comma-separated directories to pre-create in the layer (e.g. data/mesh,data/ingress/certs)")
		outputPath  = flag.String("output", "", "output tarball path (required)")
		platform    = flag.String("platform", "linux/amd64", "target platform (os/arch)")
		imageTag    = flag.String("tag", "", "image tag written into manifest.json (required)")
		entrypoint  = flag.String("entrypoint", "", "container entrypoint binary (default: --binary-dest value)")
		extraEnv    = flag.String("env", "", "comma-separated ENV=VALUE pairs to set in the image config")
	)
	flag.Parse()

	if *binaryPath == "" || *outputPath == "" || *imageTag == "" {
		log.Fatal("--binary, --output, and --tag are required")
	}

	dest := *binaryDest
	if dest == "" {
		dest = "/usr/local/bin/" + filepath.Base(*binaryPath)
	}
	ep := *entrypoint
	if ep == "" {
		ep = dest
	}

	parts := strings.SplitN(*platform, "/", 2)
	if len(parts) != 2 {
		log.Fatalf("invalid platform %q — expected os/arch", *platform)
	}
	plat := v1.Platform{OS: parts[0], Architecture: parts[1]}

	// ── Base image ────────────────────────────────────────────────────────────

	var base v1.Image
	if *baseImage == "" || *baseImage == "scratch" {
		log.Printf("using empty (scratch) base image")
		base = empty.Image
	} else {
		log.Printf("pulling %s (%s)...", *baseImage, *platform)
		ref, err := name.ParseReference(*baseImage)
		if err != nil {
			log.Fatalf("parse base image ref: %v", err)
		}
		base, err = remote.Image(ref,
			remote.WithPlatform(plat),
			remote.WithAuthFromKeychain(authn.DefaultKeychain),
		)
		if err != nil {
			log.Fatalf("pull base image: %v", err)
		}
		logImageInfo(base)
	}

	// ── Binary layer ──────────────────────────────────────────────────────────

	log.Printf("reading binary %s...", *binaryPath)
	var dirs []string
	if *extraDirs != "" {
		for _, d := range strings.Split(*extraDirs, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				dirs = append(dirs, d)
			}
		}
	}
	layer, err := binaryLayer(*binaryPath, dest, dirs)
	if err != nil {
		log.Fatalf("create layer: %v", err)
	}

	// ── Assemble image ────────────────────────────────────────────────────────

	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		log.Fatalf("append layer: %v", err)
	}

	cf, err := img.ConfigFile()
	if err != nil {
		log.Fatalf("get config: %v", err)
	}
	cf = cf.DeepCopy()
	cf.Config.Entrypoint = []string{ep}
	cf.Config.Cmd = nil
	cf.Config.User = "root"
	cf.Config.WorkingDir = "/"
	if *extraEnv != "" {
		cf.Config.Env = append(cf.Config.Env, strings.Split(*extraEnv, ",")...)
	}
	cf.OS = plat.OS
	cf.Architecture = plat.Architecture

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

	log.Printf("assembling tarball...")
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

// binaryLayer returns a v1.Layer containing the binary at destPath and any
// parent dirs needed, plus the extra pre-created directories from extraDirs.
func binaryLayer(binaryPath, destPath string, extraDirs []string) (v1.Layer, error) {
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

		// Collect all directories we must pre-create: parents of destPath plus
		// any caller-specified extra directories.
		seen := map[string]bool{}
		var allDirs []string
		for _, seg := range parentDirs(destPath) {
			if !seen[seg] {
				seen[seg] = true
				allDirs = append(allDirs, seg)
			}
		}
		for _, d := range extraDirs {
			for _, seg := range parentDirs("/" + strings.TrimPrefix(d, "/") + "/x") {
				if !seen[seg] {
					seen[seg] = true
					allDirs = append(allDirs, seg)
				}
			}
		}
		for _, d := range allDirs {
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     d,
				Mode:     0o755,
			}); err != nil {
				return nil, fmt.Errorf("write dir header %s: %w", d, err)
			}
		}

		name := strings.TrimPrefix(destPath, "/")
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
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

// parentDirs returns the dir-only path segments for path in tar-header form
// (e.g. "usr/local/bin/" for "/usr/local/bin/foo").
func parentDirs(path string) []string {
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(filepath.Dir(path), "/")
	var dirs []string
	for i := range parts {
		if parts[i] == "" || parts[i] == "." {
			continue
		}
		dirs = append(dirs, strings.Join(parts[:i+1], "/")+"/")
	}
	return dirs
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
