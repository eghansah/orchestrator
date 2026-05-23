package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func psCmd(server string, args []string) {
	fs := flag.NewFlagSet("ps", flag.ExitOnError)
	phase := fs.String("phase", "", "filter by phase: pending|scheduled|running|stopped|failed")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl ps [--phase PHASE]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	req := &gen.ListWorkloadsRequest{}
	if *phase != "" {
		p, err := parsePhase(*phase)
		if err != nil {
			die("%v", err)
		}
		req.Phases = []gen.WorkloadPhase{p}
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.ListWorkloads(c, req)
	if err != nil {
		die("list workloads: %v", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNODE\tPHASE\tKIND\tNAME\tAGE")
	for _, wl := range resp.Workloads {
		kind, name := workloadLabel(wl)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			wl.Id,
			fmtOrDash(wl.NodeId),
			fmtPhase(wl.Phase),
			kind,
			name,
			fmtAge(wl.CreatedAt),
		)
	}
	_ = tw.Flush()
}

func parsePhase(s string) (gen.WorkloadPhase, error) {
	switch s {
	case "pending":
		return gen.WorkloadPhase_PENDING, nil
	case "scheduled":
		return gen.WorkloadPhase_SCHEDULED, nil
	case "running":
		return gen.WorkloadPhase_RUNNING, nil
	case "stopped":
		return gen.WorkloadPhase_STOPPED, nil
	case "failed":
		return gen.WorkloadPhase_FAILED, nil
	default:
		return 0, fmt.Errorf("unknown phase %q; valid: pending|scheduled|running|stopped|failed", s)
	}
}
