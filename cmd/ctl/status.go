package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func statusCmd(server string, _ []string) {
	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.GetClusterState(c, &gen.GetClusterStateRequest{})
	if err != nil {
		die("get cluster state: %v", err)
	}

	fmt.Printf("Leader: %s\n\n", fmtOrDash(resp.LeaderId))

	fmt.Println("NODES")
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

	fmt.Println()
	fmt.Println("WORKLOADS")
	tw2 := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw2, "ID\tNODE\tPHASE\tKIND\tNAME\tAGE")
	for _, wl := range resp.Workloads {
		kind, name := workloadLabel(wl)
		fmt.Fprintf(tw2, "%s\t%s\t%s\t%s\t%s\t%s\n",
			wl.Id,
			fmtOrDash(wl.NodeId),
			fmtPhase(wl.Phase),
			kind,
			name,
			fmtAge(wl.CreatedAt),
		)
	}
	_ = tw2.Flush()
}
