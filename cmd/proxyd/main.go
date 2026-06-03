package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/miekg/dns"

	"github.com/eghansah/orchestrator/internal/proxycfg"
)

func main() {
	var configPath string
	var nodeID string
	flag.StringVar(&configPath, "config", "", "path to proxy config.json (required)")
	flag.StringVar(&nodeID, "node-id", "", "local node ID (required)")
	flag.Parse()

	if configPath == "" || nodeID == "" {
		fmt.Fprintln(os.Stderr, "usage: proxyd --config <path> --node-id <id>")
		os.Exit(1)
	}

	d := &daemon{
		configPath: configPath,
		nodeID:     nodeID,
		listeners:  make(map[string]listenerEntry),
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		slog.Warn("initial config load failed, waiting for file", "path", configPath, "err", err)
	} else {
		d.apply(cfg)
	}

	go d.watchConfig()

	// Block forever — the daemon exits only on fatal errors in watchConfig.
	select {}
}

// daemon owns the set of active TCP listeners and the DNS server.
type daemon struct {
	configPath string
	nodeID     string

	mu        sync.Mutex
	cfg       proxycfg.Config
	listeners map[string]listenerEntry // key: "ip:port"
	dnsStop   func()                   // stops the current DNS server pair
}

type listenerEntry struct {
	cancel func()
	entry  proxycfg.Entry
}

// watchConfig uses fsnotify to detect config file changes and reloads on each write.
// Falls back to polling every 5 seconds if fsnotify setup fails.
func (d *daemon) watchConfig() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("fsnotify unavailable, falling back to polling", "err", err)
		d.pollConfig()
		return
	}
	defer watcher.Close()

	if err := watcher.Add(d.configPath); err != nil {
		// File may not exist yet; watch the parent directory instead.
		dir := dirOf(d.configPath)
		if addErr := watcher.Add(dir); addErr != nil {
			slog.Warn("cannot watch config dir, falling back to polling", "dir", dir, "err", addErr)
			d.pollConfig()
			return
		}
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Name != d.configPath {
				continue
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				d.reload()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("fsnotify error", "err", err)
		}
	}
}

func (d *daemon) pollConfig() {
	var lastMod time.Time
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		info, err := os.Stat(d.configPath)
		if err != nil {
			continue
		}
		if info.ModTime().After(lastMod) {
			lastMod = info.ModTime()
			d.reload()
		}
	}
}

func (d *daemon) reload() {
	cfg, err := loadConfig(d.configPath)
	if err != nil {
		slog.Warn("config reload failed", "err", err)
		return
	}
	d.apply(cfg)
}

// apply reconciles active listeners and the DNS server against the new config.
func (d *daemon) apply(cfg proxycfg.Config) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// ── TCP listeners ────────────────────────────────────────────────────────

	// Build a map of desired local entries keyed by listen address.
	desired := make(map[string]proxycfg.Entry)
	for _, e := range cfg.Entries {
		if e.NodeID != d.nodeID {
			continue
		}
		if e.AllocatedPort == 0 {
			continue
		}
		addr := fmt.Sprintf("%s:%d", cfg.DataIP, e.SystemPort)
		desired[addr] = e
	}

	// Stop listeners for removed or changed entries.
	for addr, le := range d.listeners {
		want, ok := desired[addr]
		if !ok || want != le.entry {
			le.cancel()
			delete(d.listeners, addr)
		}
	}

	// Start listeners for new entries.
	for addr, e := range desired {
		if _, running := d.listeners[addr]; running {
			continue
		}
		stop := make(chan struct{})
		entry := e
		listenAddr := addr
		go func() {
			listenAndProxy(listenAddr, entry.AllocatedPort, stop)
		}()
		d.listeners[addr] = listenerEntry{
			cancel: func() { close(stop) },
			entry:  e,
		}
		slog.Info("proxy listener started", "addr", addr, "backend_port", e.AllocatedPort,
			"service", e.ServiceName, "workload", e.WorkloadName)
	}

	// ── DNS server ───────────────────────────────────────────────────────────

	dnsAddrChanged := cfg.DNSAddr != d.cfg.DNSAddr
	entriesChanged := !entriesEqual(cfg.Entries, d.cfg.Entries)

	if (dnsAddrChanged || entriesChanged || d.dnsStop == nil) && cfg.DNSAddr != "" {
		if d.dnsStop != nil {
			d.dnsStop()
		}
		d.dnsStop = startDNS(cfg)
	}

	d.cfg = cfg
	slog.Info("config applied", "services", len(desired), "dns", cfg.DNSAddr)
}

// listenAndProxy accepts connections on addr and forwards each to 127.0.0.1:backendPort.
func listenAndProxy(addr string, backendPort uint32, stop <-chan struct{}) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("proxy listen failed", "addr", addr, "err", err)
		return
	}
	go func() {
		<-stop
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // stop channel closed or fatal error
		}
		go handleConn(conn, backendPort)
	}
}

func handleConn(conn net.Conn, backendPort uint32) {
	defer conn.Close()
	backend, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", backendPort))
	if err != nil {
		slog.Warn("proxy dial backend failed", "port", backendPort, "err", err)
		return
	}
	defer backend.Close()
	copyBidirectional(conn, backend)
}

func copyBidirectional(a, b net.Conn) {
	type halfCloser interface{ CloseWrite() error }
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		if hc, ok := a.(halfCloser); ok {
			_ = hc.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		if hc, ok := b.(halfCloser); ok {
			_ = hc.CloseWrite()
		}
	}()
	wg.Wait()
}

// ── DNS ───────────────────────────────────────────────────────────────────────

// startDNS launches UDP and TCP DNS listeners for the given config.
// Returns a stop function that shuts both down.
func startDNS(cfg proxycfg.Config) func() {
	// Build lookup table: "service.workload" → entry
	table := make(map[string]proxycfg.Entry, len(cfg.Entries))
	for _, e := range cfg.Entries {
		key := strings.ToLower(e.ServiceName + "." + e.WorkloadName)
		table[key] = e
	}

	mux := dns.NewServeMux()
	mux.HandleFunc("svc.local.", makeSvcLocalHandler(table))
	mux.HandleFunc(".", handleForward)

	udpSrv := &dns.Server{Addr: cfg.DNSAddr, Net: "udp", Handler: mux}
	tcpSrv := &dns.Server{Addr: cfg.DNSAddr, Net: "tcp", Handler: mux}

	go func() {
		if err := udpSrv.ListenAndServe(); err != nil {
			slog.Error("DNS UDP server error", "err", err)
		}
	}()
	go func() {
		if err := tcpSrv.ListenAndServe(); err != nil {
			slog.Error("DNS TCP server error", "err", err)
		}
	}()

	slog.Info("DNS server started", "addr", cfg.DNSAddr)

	return func() {
		_ = udpSrv.Shutdown()
		_ = tcpSrv.Shutdown()
	}
}

func makeSvcLocalHandler(table map[string]proxycfg.Entry) dns.HandlerFunc {
	return func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true

		if len(r.Question) == 0 {
			_ = w.WriteMsg(m)
			return
		}

		q := r.Question[0]
		qname := strings.ToLower(q.Name) // e.g. "api.myapp.svc.local."

		// Strip ".svc.local." suffix → "api.myapp"
		label := strings.TrimSuffix(qname, ".svc.local.")

		switch q.Qtype {
		case dns.TypeA:
			if e, ok := table[label]; ok {
				if ip := net.ParseIP(e.NodeDataIP).To4(); ip != nil {
					m.Answer = append(m.Answer, &dns.A{
						Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 10},
						A:   ip,
					})
				}
			}

		case dns.TypeSRV:
			// _<service>._tcp.<workload>.svc.local. → strip "_" and "._tcp"
			svcPart := strings.TrimPrefix(label, "_")
			svcPart = strings.TrimSuffix(svcPart, "._tcp")
			if e, ok := table[svcPart]; ok {
				target := e.ServiceName + "." + e.WorkloadName + ".svc.local."
				m.Answer = append(m.Answer, &dns.SRV{
					Hdr:      dns.RR_Header{Name: q.Name, Rrtype: dns.TypeSRV, Class: dns.ClassINET, Ttl: 10},
					Priority: 10,
					Weight:   100,
					Port:     uint16(e.SystemPort),
					Target:   target,
				})
				if ip := net.ParseIP(e.NodeDataIP).To4(); ip != nil {
					m.Extra = append(m.Extra, &dns.A{
						Hdr: dns.RR_Header{Name: target, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 10},
						A:   ip,
					})
				}
			}
		}

		if len(m.Answer) == 0 {
			m.Rcode = dns.RcodeNameError
		}
		_ = w.WriteMsg(m)
	}
}

// handleForward proxies non-svc.local queries to upstream nameservers.
func handleForward(w dns.ResponseWriter, r *dns.Msg) {
	client := &dns.Client{Timeout: 4 * time.Second}
	for _, ns := range readUpstreams() {
		resp, _, err := client.Exchange(r, ns)
		if err != nil {
			continue
		}
		resp.Id = r.Id
		_ = w.WriteMsg(resp)
		return
	}
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeServerFailure
	_ = w.WriteMsg(m)
}

func readUpstreams() []string {
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
		if len(fields) >= 2 {
			servers = append(servers, net.JoinHostPort(fields[1], "53"))
		}
	}
	if len(servers) == 0 {
		return []string{"8.8.8.8:53"}
	}
	return servers
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func loadConfig(path string) (proxycfg.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return proxycfg.Config{}, err
	}
	var cfg proxycfg.Config
	return cfg, json.Unmarshal(data, &cfg)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

func entriesEqual(a, b []proxycfg.Entry) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]proxycfg.Entry, len(a))
	for _, e := range a {
		m[e.ServiceName+"."+e.WorkloadName] = e
	}
	for _, e := range b {
		if m[e.ServiceName+"."+e.WorkloadName] != e {
			return false
		}
	}
	return true
}
