package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

const defaultServer = "localhost:7946"

var dialTimeout = 10 * time.Second

func main() {
	server := defaultServer
	args := os.Args[1:]

	// Consume --server/-server before the subcommand.
	for len(args) >= 2 && (args[0] == "--server" || args[0] == "-server") {
		server = args[1]
		args = args[2:]
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(0)
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		runContainerCmd(server, rest)
	case "stack":
		runStackCmd(server, rest)
	case "ps":
		psCmd(server, rest)
	case "nodes":
		nodesCmd(server, rest)
	case "rm":
		rmCmd(server, rest)
	case "drain":
		drainCmd(server, rest)
	case "status":
		statusCmd(server, rest)
	case "ingress":
		ingressCmd(server, rest)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "ctl: unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`ctl — orchestrator CLI

Usage:
  ctl [--server HOST:PORT] COMMAND [flags]

Commands:
  run     Submit a container workload
  stack   Submit a compose stack workload
  ps      List workloads
  nodes   List cluster nodes
  rm      Remove workloads by ID
  drain   Mark a node as draining
  status  Show full cluster state
  ingress Manage ingress routing rules

Flags:
  --server HOST:PORT   orchestrator gRPC address (default: localhost:7946)

Run 'ctl COMMAND --help' for per-command flags.
`)
}

// dial returns a gRPC connection and a ready ControlServiceClient.
// Uses TLS without cert verification — ctl is a local admin tool and the
// server's cert is self-signed. Node-to-node mTLS uses full cert pinning.
// The caller is responsible for closing the connection.
func dial(server string) (*grpc.ClientConn, gen.ControlServiceClient) {
	tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	conn, err := grpc.NewClient(server, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		die("connect to %s: %v", server, err)
	}
	return conn, gen.NewControlServiceClient(conn)
}

// reqCtx returns a context with a 10-second deadline, suitable for a single RPC.
func reqCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), dialTimeout)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

// ── Shared formatters ─────────────────────────────────────────────────────────

func fmtPhase(p gen.WorkloadPhase) string {
	switch p {
	case gen.WorkloadPhase_PENDING:
		return "pending"
	case gen.WorkloadPhase_SCHEDULED:
		return "scheduled"
	case gen.WorkloadPhase_RUNNING:
		return "running"
	case gen.WorkloadPhase_STOPPED:
		return "stopped"
	case gen.WorkloadPhase_FAILED:
		return "failed"
	default:
		return "unknown"
	}
}

func fmtNodeStatus(s gen.NodeStatus) string {
	switch s {
	case gen.NodeStatus_HEALTHY:
		return "healthy"
	case gen.NodeStatus_DRAINING:
		return "draining"
	case gen.NodeStatus_UNREACHABLE:
		return "unreachable"
	default:
		return "unknown"
	}
}

func fmtAge(unixSec int64) string {
	if unixSec == 0 {
		return "—"
	}
	d := time.Since(time.Unix(unixSec, 0))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours()/24), int(d.Hours())%24)
	}
}

func fmtOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// workloadLabel extracts (kind, name) from a proto Workload for display.
func workloadLabel(wl *gen.Workload) (kind, name string) {
	switch s := wl.Spec.(type) {
	case *gen.Workload_Container:
		return "container", s.Container.Name
	case *gen.Workload_Stack:
		return "stack", s.Stack.Name
	}
	return "unknown", "—"
}
