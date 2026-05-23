package tlsutil

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// ServerTLSConfig returns a tls.Config for the gRPC server.
//
// The server presents ownCert. Client certs are optional at the TLS level
// (Heartbeat doesn't present one during join). When a client cert IS presented,
// isPinned is called with the raw DER bytes; if it returns false the handshake
// is rejected. Methods that require a cert are enforced by NodeAuthInterceptor.
func ServerTLSConfig(ownCert tls.Certificate, isPinned func(certDER []byte) bool) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{ownCert},
		ClientAuth:   tls.RequestClientCert,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return nil
			}
			if !isPinned(rawCerts[0]) {
				return errors.New("client certificate not registered in cluster")
			}
			return nil
		},
	}
}

// ClientTLSConfig returns a tls.Config for a gRPC client connecting to a peer.
//
// The client presents ownCert. If serverCertDER is non-nil the server's cert is
// verified against that exact DER (key pinning). If serverCertDER is nil the
// server cert is not verified — use this only for the initial TOFU join heartbeat.
func ClientTLSConfig(ownCert tls.Certificate, serverCertDER []byte) *tls.Config {
	cfg := &tls.Config{
		Certificates:       []tls.Certificate{ownCert},
		InsecureSkipVerify: true, // we do our own pinning below
	}
	if serverCertDER != nil {
		pinned := serverCertDER
		cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("server presented no certificate")
			}
			if !bytes.Equal(rawCerts[0], pinned) {
				return errors.New("server certificate does not match pinned cert")
			}
			return nil
		}
	}
	return cfg
}

// NodeAuthInterceptor is a gRPC unary server interceptor that enforces mTLS
// for protected NodeService methods. The Heartbeat RPC is intentionally excluded
// because joining nodes are not yet registered and present no client cert.
func NodeAuthInterceptor(isPinned func(certDER []byte) bool) grpc.UnaryServerInterceptor {
	protected := map[string]struct{}{
		"/orchestrator.NodeService/PlaceWorkload":  {},
		"/orchestrator.NodeService/RemoveWorkload": {},
		"/orchestrator.NodeService/ReportState":    {},
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, ok := protected[info.FullMethod]; !ok {
			return handler(ctx, req)
		}
		p, ok := peer.FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "no peer info")
		}
		tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
		if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
			return nil, status.Error(codes.Unauthenticated, "mTLS client certificate required")
		}
		if !isPinned(tlsInfo.State.PeerCertificates[0].Raw) {
			return nil, status.Error(codes.Unauthenticated, "client certificate not registered in cluster")
		}
		return handler(ctx, req)
	}
}
