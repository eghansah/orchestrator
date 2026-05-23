package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func nodesCmd(server string, args []string) {
	fs := flag.NewFlagSet("nodes", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "Usage: ctl nodes") }
	_ = fs.Parse(args)

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.ListNodes(c, &gen.ListNodesRequest{})
	if err != nil {
		die("list nodes: %v", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tADDRESS\tSTATUS\tCPUS")
	for _, n := range resp.Nodes {
		cpus := "—"
		if n.Resources != nil && n.Resources.CpuCores > 0 {
			cpus = fmt.Sprintf("%d", n.Resources.CpuCores)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			n.NodeId, n.Address, fmtNodeStatus(n.Status), cpus,
		)
	}
	_ = tw.Flush()
}

func drainCmd(server string, args []string) {
	fs := flag.NewFlagSet("drain", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl drain NODE_ID")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "error: NODE_ID is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.DrainNode(c, &gen.DrainNodeRequest{NodeId: fs.Arg(0)})
	if err != nil {
		die("drain node: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	fmt.Printf("draining %s\n", fs.Arg(0))
}
