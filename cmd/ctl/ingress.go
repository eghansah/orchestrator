package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func ingressCmd(server string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ctl ingress <list|create|delete> [flags]")
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		ingressListCmd(server, rest)
	case "create":
		ingressCreateCmd(server, rest)
	case "delete":
		ingressDeleteCmd(server, rest)
	default:
		fmt.Fprintf(os.Stderr, "ctl ingress: unknown subcommand %q\n", sub)
		os.Exit(1)
	}
}

func ingressListCmd(server string, args []string) {
	fs := flag.NewFlagSet("ingress list", flag.ExitOnError)
	_ = fs.Parse(args)

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.ListIngress(c, &gen.ListIngressRequest{})
	if err != nil {
		die("list ingress: %v", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tHOST\tPATH\tWORKLOAD\tPORT\tAGE")
	for _, r := range resp.Rules {
		host := fmtOrDash(r.Host)
		path := r.PathPrefix
		if path == "" {
			path = "/"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n",
			r.Id, host, path, r.WorkloadId, r.Port, fmtAge(r.CreatedAt),
		)
	}
	_ = tw.Flush()
}

func ingressCreateCmd(server string, args []string) {
	fs := flag.NewFlagSet("ingress create", flag.ExitOnError)
	host := fs.String("host", "", "Host header to match (empty = match all)")
	path := fs.String("path", "/", "URL path prefix to match")
	workload := fs.String("workload", "", "workload ID to route to")
	port := fs.Uint("port", 0, "host port on the target node")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl ingress create --workload ID --port PORT [--host HOST] [--path PREFIX]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *workload == "" || *port == 0 {
		fmt.Fprintln(os.Stderr, "error: --workload and --port are required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.CreateIngress(c, &gen.CreateIngressRequest{
		Host:       *host,
		PathPrefix: *path,
		WorkloadId: *workload,
		Port:       uint32(*port),
	})
	if err != nil {
		die("create ingress: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	fmt.Printf("created ingress rule %s\n", resp.RuleId)
}

func ingressDeleteCmd(server string, args []string) {
	fs := flag.NewFlagSet("ingress delete", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl ingress delete RULE_ID [RULE_ID...]")
	}
	_ = fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one RULE_ID is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()

	for _, id := range fs.Args() {
		c, cancel := reqCtx()
		resp, err := client.DeleteIngress(c, &gen.DeleteIngressRequest{RuleId: id})
		cancel()
		if err != nil {
			die("delete ingress %s: %v", id, err)
		}
		if !resp.Accepted {
			die("rejected: %s", resp.Reason)
		}
		fmt.Printf("deleted %s\n", id)
	}
}
