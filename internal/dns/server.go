package dns

import (
	"bufio"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"

	internraft "github.com/eghansah/orchestrator/internal/raft"
)

// Server answers DNS queries for the svc.local zone and forwards everything
// else to the upstream resolvers from /etc/resolv.conf.
type Server struct {
	peer   *internraft.Peer
	dataIP string
	mux    *dns.ServeMux
}

func New(peer *internraft.Peer, dataIP string) *Server {
	s := &Server{peer: peer, dataIP: dataIP, mux: dns.NewServeMux()}
	s.mux.HandleFunc("svc.local.", s.handleSvcLocal)
	s.mux.HandleFunc(".", s.handleForward)
	return s
}

// ListenAndServe starts UDP and TCP listeners on addr (e.g. ":53").
func (s *Server) ListenAndServe(addr string) error {
	errCh := make(chan error, 2)
	go func() { errCh <- dns.ListenAndServe(addr, "udp", s.mux) }()
	go func() { errCh <- dns.ListenAndServe(addr, "tcp", s.mux) }()
	return <-errCh
}

// handleSvcLocal answers A and SRV queries for <name>.svc.local.
func (s *Server) handleSvcLocal(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	if len(r.Question) == 0 {
		_ = w.WriteMsg(m)
		return
	}

	q := r.Question[0]
	qname := strings.ToLower(q.Name) // e.g. "api.svc.local."

	// Extract the service name label (everything before .svc.local.).
	label := strings.TrimSuffix(qname, ".svc.local.")

	state := s.peer.State()

	switch q.Qtype {
	case dns.TypeA:
		for _, svc := range state.Services {
			if svc.Name == label {
				ip := net.ParseIP(s.dataIP).To4()
				if ip == nil {
					break
				}
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 10},
					A:   ip,
				})
				break
			}
		}

	case dns.TypeSRV:
		// SRV query: _<name>._tcp.svc.local. — label starts with "_"
		svcName := strings.TrimPrefix(label, "_")
		svcName = strings.TrimSuffix(svcName, "._tcp")
		for _, svc := range state.Services {
			if svc.Name == svcName {
				target := svcName + ".svc.local."
				m.Answer = append(m.Answer, &dns.SRV{
					Hdr:      dns.RR_Header{Name: q.Name, Rrtype: dns.TypeSRV, Class: dns.ClassINET, Ttl: 10},
					Priority: 10,
					Weight:   100,
					Port:     uint16(svc.SystemPort),
					Target:   target,
				})
				// Add A record in Additional.
				ip := net.ParseIP(s.dataIP).To4()
				if ip != nil {
					m.Extra = append(m.Extra, &dns.A{
						Hdr: dns.RR_Header{Name: target, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 10},
						A:   ip,
					})
				}
				break
			}
		}
	}

	if len(m.Answer) == 0 {
		m.Rcode = dns.RcodeNameError // NXDOMAIN
	}
	_ = w.WriteMsg(m)
}

// handleForward proxies queries to the upstream nameservers from /etc/resolv.conf.
func (s *Server) handleForward(w dns.ResponseWriter, r *dns.Msg) {
	upstreams := readUpstreamServers()
	client := &dns.Client{Timeout: 4 * time.Second}

	for _, ns := range upstreams {
		resp, _, err := client.Exchange(r, ns)
		if err != nil {
			slog.Debug("dns forward failed", "ns", ns, "err", err)
			continue
		}
		resp.Id = r.Id
		_ = w.WriteMsg(resp)
		return
	}

	// All upstreams failed — return SERVFAIL.
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeServerFailure
	_ = w.WriteMsg(m)
}

// readUpstreamServers parses nameservers from /etc/resolv.conf.
func readUpstreamServers() []string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return []string{"8.8.8.8:53"}
	}
	defer f.Close()

	var servers []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		servers = append(servers, net.JoinHostPort(fields[1], "53"))
	}
	if len(servers) == 0 {
		return []string{"8.8.8.8:53"}
	}
	return servers
}
