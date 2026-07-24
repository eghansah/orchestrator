package webui

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"time"
)

// traefikDefaultCertCN is the Subject Common Name k3s's bundled Traefik
// (and stock Traefik generally) serves when no IngressRoute/cert matches the
// request. It is a stable, well-known fingerprint: unlike a real listener,
// k3s's ServiceLB/klipper-lb intercepts hostPort 443 via iptables DNAT rather
// than binding a host socket, so `ss -tlnp`/`lsof` show the port as free even
// though a TLS handshake against it succeeds and returns this cert. This
// orchestrator is unprivileged and can never bind :443 itself (no
// CAP_NET_BIND_SERVICE), so bind()-based "is the port free" checks are both
// impossible and useless here; a live TLS probe is the only reliable signal.
const traefikDefaultCertCN = "TRAEFIK DEFAULT CERT"

// checkPort443Conflict probes localhost:443 and reports a warning if the
// certificate presented there looks like it belongs to another cluster's
// ingress controller rather than this node's own ingress stack (host
// HAProxy / ingressd), which never serves that certificate.
func checkPort443Conflict() []string {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:443", 1500*time.Millisecond)
	if err != nil {
		return nil
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // diagnostic probe only, not a trust decision
	if err := tlsConn.Handshake(); err != nil {
		return nil
	}
	defer tlsConn.Close()

	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil
	}
	cn := certs[0].Subject.CommonName
	if cn != traefikDefaultCertCN {
		return nil
	}

	if detectK3s() {
		return []string{
			"k3s appears to have stolen port 443 from this node's host HAProxy. k3s's bundled Traefik/ServiceLB " +
				"(klipper-lb) intercepts hostPort 443 using iptables DNAT rather than binding a normal host socket, so " +
				"it never shows up as a listener in `ss`/`lsof` and standard port-conflict checks miss it entirely — " +
				"this was only caught by a live TLS probe, which got back Traefik's default certificate " +
				"(CN=\"" + traefikDefaultCertCN + "\") instead of one of this cluster's own certs. " +
				"In this state, external traffic to :443 is silently redirected into k3s and never reaches the host " +
				"HAProxy or ingressd — web services and ingress rules configured on this cluster can appear healthy " +
				"while actually being unreachable. Uninstall or stop k3s's ServiceLB/Traefik on this node, or move " +
				"this cluster's ingress to different ports.",
		}
	}
	return []string{
		fmt.Sprintf(
			"Something is intercepting port 443 before it reaches this node's host HAProxy: a live TLS probe got back "+
				"a Traefik default certificate (CN=%q) instead of one of this cluster's own certs. This is commonly "+
				"caused by k3s's bundled Traefik/ServiceLB, which grabs hostPort 443 via iptables DNAT rather than a "+
				"normal host socket — so it won't appear as a listener in `ss`/`lsof`, and the port looks free even "+
				"though it isn't. In this state, external traffic to :443 is silently redirected away and never "+
				"reaches the host HAProxy or ingressd — web services and ingress rules configured on this cluster can "+
				"appear healthy while actually being unreachable.",
			cn,
		),
	}
}

// detectK3s looks for common, unprivileged, read-only signs of a k3s
// install on this host. None of these require root or CAP_NET_ADMIN.
func detectK3s() bool {
	for _, p := range []string{"/etc/rancher/k3s/k3s.yaml", "/usr/local/bin/k3s"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return slices.ContainsFunc([]string{"k3s", "k3s-server", "k3s-agent"}, isHostProcessRunning)
}

func (s *Server) handleSystemDiagnostics(w http.ResponseWriter, _ *http.Request) {
	warnings := checkPort443Conflict()
	warnings = append(warnings, checkMeshCIDROverlap(s.peer.State().MeshCIDR)...)
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, map[string][]string{"warnings": warnings})
}
