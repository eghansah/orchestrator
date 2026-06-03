package agent

import (
	"fmt"
	"log/slog"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

type halfCloser interface{ CloseWrite() error }

// ForwardTCP implements NodeServiceServer. The caller sends one AllocatedPort
// message first, then streams raw bytes. This node dials 127.0.0.1:port and
// proxies bytes in both directions.
func (a *Agent) ForwardTCP(stream grpc.BidiStreamingServer[gen.ForwardTCPRequest, gen.ForwardTCPChunk]) error {
	first, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "receive port: %v", err)
	}
	allocatedPort := first.GetAllocatedPort()
	if allocatedPort == 0 {
		return status.Error(codes.InvalidArgument, "first message must contain allocated_port")
	}

	backend, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", allocatedPort))
	if err != nil {
		slog.Warn("ForwardTCP dial failed", "port", allocatedPort, "err", err)
		return status.Errorf(codes.Unavailable, "dial %d: %v", allocatedPort, err)
	}
	defer backend.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	// stream → backend: relay bytes from the remote caller to the local container.
	go func() {
		defer wg.Done()
		for {
			req, err := stream.Recv()
			if err != nil {
				// Remote caller is done sending; signal EOF to the backend so it
				// can flush and close its side of the connection.
				if hc, ok := backend.(halfCloser); ok {
					_ = hc.CloseWrite()
				}
				return
			}
			if data := req.GetData(); len(data) > 0 {
				if _, werr := backend.Write(data); werr != nil {
					return
				}
			}
		}
	}()

	// backend → stream: relay bytes from the local container back to the caller.
	go func() {
		defer wg.Done()
		// Closing the backend when this goroutine exits unblocks any concurrent
		// Write() in the other goroutine so both goroutines can finish cleanly.
		defer backend.Close()
		buf := make([]byte, 32*1024)
		for {
			n, err := backend.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				if serr := stream.Send(&gen.ForwardTCPChunk{Data: chunk}); serr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	wg.Wait()
	return nil
}
