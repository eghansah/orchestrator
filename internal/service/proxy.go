package service

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
	internraft "github.com/eghansah/orchestrator/internal/raft"
	"github.com/eghansah/orchestrator/internal/tlsutil"
	"github.com/eghansah/orchestrator/pkg/types"
)

const reconcileInterval = 5 * time.Second

// Manager maintains one TCP listener per service. For local containers it dials
// loopback directly; for remote containers it tunnels via the ForwardTCP gRPC stream.
type Manager struct {
	peer        *internraft.Peer
	localNodeID string
	dataIP      string
	ownCert     tls.Certificate

	mu       sync.Mutex
	active   map[string]context.CancelFunc // serviceID → cancel
}

func New(peer *internraft.Peer, localNodeID, dataIP string, ownCert tls.Certificate) *Manager {
	return &Manager{
		peer:        peer,
		localNodeID: localNodeID,
		dataIP:      dataIP,
		ownCert:     ownCert,
		active:      make(map[string]context.CancelFunc),
	}
}

// Run starts the reconcile loop. Blocks until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		m.reconcile(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) reconcile(ctx context.Context) {
	state := m.peer.State()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Stop listeners for removed services.
	for id, cancel := range m.active {
		if _, ok := state.Services[id]; !ok {
			cancel()
			delete(m.active, id)
		}
	}

	// Start listeners for new services.
	for id, svc := range state.Services {
		if _, ok := m.active[id]; ok {
			continue
		}
		svcCtx, cancel := context.WithCancel(ctx)
		m.active[id] = cancel
		go m.listenService(svcCtx, svc)
	}
}

func (m *Manager) listenService(ctx context.Context, svc types.Service) {
	addr := fmt.Sprintf("%s:%d", m.dataIP, svc.SystemPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("service listen failed", "service", svc.Name, "addr", addr, "err", err)
		return
	}
	slog.Info("service listener started", "service", svc.Name, "addr", addr)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return // context cancelled
			}
			slog.Error("service accept failed", "service", svc.Name, "err", err)
			return
		}
		go m.handleConn(ctx, conn, svc)
	}
}

func (m *Manager) handleConn(ctx context.Context, conn net.Conn, svc types.Service) {
	defer conn.Close()

	state := m.peer.State()
	var wl types.Workload
	var ok bool
	for _, w := range state.Workloads {
		if w.Name() == svc.WorkloadName {
			wl = w
			ok = true
			break
		}
	}
	if !ok || wl.NodeID == "" {
		slog.Warn("service workload not scheduled", "service", svc.Name)
		return
	}

	allocatedPort := allocatedPortFor(wl, svc.TargetPort)
	if allocatedPort == 0 {
		slog.Warn("service no allocated port", "service", svc.Name, "target_port", svc.TargetPort)
		return
	}

	if wl.NodeID == m.localNodeID {
		m.proxyLocal(ctx, conn, allocatedPort)
		return
	}

	node, ok := state.Nodes[wl.NodeID]
	if !ok {
		slog.Warn("service target node not found", "service", svc.Name, "node", wl.NodeID)
		return
	}
	m.proxyRemote(ctx, conn, node, allocatedPort)
}

func (m *Manager) proxyLocal(_ context.Context, conn net.Conn, allocatedPort uint32) {
	backend, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", allocatedPort))
	if err != nil {
		slog.Error("service local dial failed", "port", allocatedPort, "err", err)
		return
	}
	defer backend.Close()
	copyBidirectional(conn, backend)
}

func (m *Manager) proxyRemote(ctx context.Context, conn net.Conn, node types.Node, allocatedPort uint32) {
	tlsCfg := tlsutil.ClientTLSConfig(m.ownCert, node.TLSCert)
	grpcConn, err := grpc.NewClient(node.Address, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		slog.Error("service remote dial failed", "node", node.ID, "err", err)
		return
	}
	defer grpcConn.Close()

	stream, err := gen.NewNodeServiceClient(grpcConn).ForwardTCP(ctx)
	if err != nil {
		slog.Error("service ForwardTCP open failed", "node", node.ID, "err", err)
		return
	}

	// First message: which port to dial on the remote node.
	if err := stream.Send(&gen.ForwardTCPRequest{
		Payload: &gen.ForwardTCPRequest_AllocatedPort{AllocatedPort: allocatedPort},
	}); err != nil {
		slog.Error("service ForwardTCP send port failed", "err", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// conn → stream
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				if sendErr := stream.Send(&gen.ForwardTCPRequest{
					Payload: &gen.ForwardTCPRequest_Data{Data: chunk},
				}); sendErr != nil {
					return
				}
			}
			if err != nil {
				_ = stream.CloseSend()
				return
			}
		}
	}()

	// stream → conn
	go func() {
		defer wg.Done()
		for {
			chunk, err := stream.Recv()
			if err != nil {
				return
			}
			if _, writeErr := conn.Write(chunk.Data); writeErr != nil {
				return
			}
		}
	}()

	wg.Wait()
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

func allocatedPortFor(wl types.Workload, containerPort uint32) uint32 {
	for _, pa := range wl.PortAllocations {
		if pa.ContainerPort == containerPort {
			return pa.AllocatedPort
		}
	}
	return 0
}
