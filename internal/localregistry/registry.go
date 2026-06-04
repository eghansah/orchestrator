// Package localregistry implements a minimal read-only Docker Registry V2 server
// backed by an in-memory content store loaded from a Docker-save tarball.
// It is used to serve the embedded ingressd image to nerdctl on the local node.
package localregistry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Registry serves a read-only Docker Registry V2 API.
// All blobs are held in memory.
type Registry struct {
	blobs      map[string][]byte  // "sha256:<hex>" → bytes
	mediaTypes map[string]string  // "sha256:<hex>" → MIME type
	manifests  map[string][]byte  // "sha256:<hex>" → manifest JSON
	tags       map[string]string  // "<name>:<tag>" → manifest digest "sha256:<hex>"
}

// New parses a Docker-save tarball and returns a Registry serving its image(s).
// If tarData is nil or empty, a no-op registry is returned (useful when the
// embedded image was not included at build time).
func New(tarData []byte) (*Registry, error) {
	r := &Registry{
		blobs:      make(map[string][]byte),
		mediaTypes: make(map[string]string),
		manifests:  make(map[string][]byte),
		tags:       make(map[string]string),
	}
	if len(tarData) == 0 {
		return r, nil
	}
	if err := r.loadDockerTar(bytes.NewReader(tarData)); err != nil {
		return nil, fmt.Errorf("parse image tarball: %w", err)
	}
	return r, nil
}

// Handler returns an http.Handler implementing the Registry V2 API.
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/{$}", r.handlePing)
	mux.HandleFunc("HEAD /v2/{$}", r.handlePing)
	mux.HandleFunc("GET /v2/{name}/manifests/{ref}", r.handleManifest)
	mux.HandleFunc("HEAD /v2/{name}/manifests/{ref}", r.handleManifest)
	mux.HandleFunc("GET /v2/{name}/blobs/{digest}", r.handleBlob)
	mux.HandleFunc("HEAD /v2/{name}/blobs/{digest}", r.handleBlob)
	return mux
}

// ImageCount returns the number of image manifests loaded.
func (r *Registry) ImageCount() int { return len(r.tags) }

// ── HTTP handlers ─────────────────────────────────────────────────────────────

func (r *Registry) handlePing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func (r *Registry) handleManifest(w http.ResponseWriter, req *http.Request) {
	name := req.PathValue("name")
	ref := req.PathValue("ref")

	var manifestBytes []byte

	// Try tag lookup first ("name:ref"), then digest lookup.
	if digest, ok := r.tags[name+":"+ref]; ok {
		manifestBytes = r.manifests[digest]
	} else if strings.HasPrefix(ref, "sha256:") {
		manifestBytes = r.manifests[ref]
	}

	if manifestBytes == nil {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}

	mt := "application/vnd.docker.distribution.manifest.v2+json"
	digest := contentDigest(manifestBytes)

	w.Header().Set("Content-Type", mt)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(manifestBytes)))
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")

	if req.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(manifestBytes)
}

func (r *Registry) handleBlob(w http.ResponseWriter, req *http.Request) {
	digest := req.PathValue("digest")

	data, ok := r.blobs[digest]
	if !ok {
		registryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}

	mt := r.mediaTypes[digest]
	if mt == "" {
		mt = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")

	if req.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ── Docker-save tar parser ────────────────────────────────────────────────────

// dockerSaveManifest is one entry in manifest.json from docker save.
type dockerSaveManifest struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

// v2Manifest is the Docker Registry V2 image manifest.
type v2Manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        v2Descriptor `json:"config"`
	Layers        []v2Descriptor `json:"layers"`
}

type v2Descriptor struct {
	MediaType string `json:"mediaType"`
	Size      int    `json:"size"`
	Digest    string `json:"digest"`
}

func (r *Registry) loadDockerTar(rd io.Reader) error {
	// First pass: collect all file contents.
	files := make(map[string][]byte)
	tr := tar.NewReader(rd)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return fmt.Errorf("read tar entry %s: %w", hdr.Name, err)
		}
		// Normalise path: trim leading "./"
		name := strings.TrimPrefix(hdr.Name, "./")
		files[name] = data
	}

	manifestData, ok := files["manifest.json"]
	if !ok {
		return fmt.Errorf("manifest.json not found in tarball")
	}

	var entries []dockerSaveManifest
	if err := json.Unmarshal(manifestData, &entries); err != nil {
		return fmt.Errorf("parse manifest.json: %w", err)
	}

	for _, entry := range entries {
		if err := r.loadEntry(files, entry); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) loadEntry(files map[string][]byte, entry dockerSaveManifest) error {
	// Load config blob.
	configPath := strings.TrimPrefix(entry.Config, "./")
	configData, ok := files[configPath]
	if !ok {
		return fmt.Errorf("config file %q not found in tarball", entry.Config)
	}
	configDigest := blobDigest(configData)
	r.blobs[configDigest] = configData
	r.mediaTypes[configDigest] = "application/vnd.docker.container.image.v1+json"

	// Load and gzip-compress each layer.
	var layerDescs []v2Descriptor
	for _, layerPath := range entry.Layers {
		layerPath = strings.TrimPrefix(layerPath, "./")
		layerData, ok := files[layerPath]
		if !ok {
			return fmt.Errorf("layer file %q not found in tarball", layerPath)
		}
		compressed, err := gzipCompress(layerData)
		if err != nil {
			return fmt.Errorf("compress layer %s: %w", layerPath, err)
		}
		layerDigest := blobDigest(compressed)
		r.blobs[layerDigest] = compressed
		r.mediaTypes[layerDigest] = "application/vnd.docker.image.rootfs.diff.tar.gzip"
		layerDescs = append(layerDescs, v2Descriptor{
			MediaType: "application/vnd.docker.image.rootfs.diff.tar.gzip",
			Size:      len(compressed),
			Digest:    layerDigest,
		})
	}

	// Build the V2 manifest.
	manifest := v2Manifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.docker.distribution.manifest.v2+json",
		Config: v2Descriptor{
			MediaType: "application/vnd.docker.container.image.v1+json",
			Size:      len(configData),
			Digest:    configDigest,
		},
		Layers: layerDescs,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	manifestDigest := contentDigest(manifestBytes)
	r.manifests[manifestDigest] = manifestBytes

	// Register all repo tags.
	for _, tag := range entry.RepoTags {
		// Strip registry prefix if present (e.g. "docker.io/library/ingressd:latest" → "ingressd:latest").
		if idx := strings.LastIndex(tag, "/"); idx >= 0 {
			tag = tag[idx+1:]
		}
		r.tags[tag] = manifestDigest
	}

	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func blobDigest(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", h)
}

func contentDigest(data []byte) string { return blobDigest(data) }

// gzipCompress returns data wrapped in a gzip stream.
// If data already starts with the gzip magic bytes it is returned as-is.
func gzipCompress(data []byte) ([]byte, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return data, nil // already gzip
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func registryError(w http.ResponseWriter, code int, errCode, msg string) {
	type regErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	type errResp struct {
		Errors []regErr `json:"errors"`
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(errResp{Errors: []regErr{{Code: errCode, Message: msg}}})
}
