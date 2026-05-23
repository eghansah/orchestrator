package main

import (
	"flag"
	"fmt"
	"os"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func rmCmd(server string, args []string) {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl rm WORKLOAD_ID [WORKLOAD_ID...]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "error: at least one WORKLOAD_ID is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()

	exitCode := 0
	for _, id := range fs.Args() {
		c, cancel := reqCtx()
		resp, err := client.RemoveWorkload(c, &gen.RemoveWorkloadRequest{WorkloadId: id})
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", id, err)
			exitCode = 1
			continue
		}
		if !resp.Accepted {
			fmt.Fprintf(os.Stderr, "%s: rejected: %s\n", id, resp.Reason)
			exitCode = 1
			continue
		}
		fmt.Println(id)
	}
	os.Exit(exitCode)
}
