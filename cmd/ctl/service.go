package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func serviceCmd(server string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ctl service <list|create|delete> [flags]")
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		serviceListCmd(server, rest)
	case "create":
		serviceCreateCmd(server, rest)
	case "delete":
		serviceDeleteCmd(server, rest)
	default:
		fmt.Fprintf(os.Stderr, "ctl service: unknown subcommand %q\n", sub)
		os.Exit(1)
	}
}

func serviceListCmd(server string, args []string) {
	fs := flag.NewFlagSet("service list", flag.ExitOnError)
	_ = fs.Parse(args)

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.ListService(c, &gen.ListServiceRequest{})
	if err != nil {
		die("list services: %v", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSYSTEM PORT\tWORKLOAD NAME\tTARGET PORT\tAGE")
	for _, s := range resp.Services {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%d\t%s\n",
			s.Id, s.Name, s.SystemPort, s.WorkloadName, s.TargetPort, fmtAge(s.CreatedAt),
		)
	}
	_ = tw.Flush()
}

func serviceCreateCmd(server string, args []string) {
	fs := flag.NewFlagSet("service create", flag.ExitOnError)
	name := fs.String("name", "", "service name (DNS label, e.g. \"api\")")
	workload := fs.String("workload", "", "workload name to route to (Container.Name or Stack.Name)")
	port := fs.Uint("port", 0, "container port to proxy to")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl service create --name NAME --workload WORKLOAD_NAME --port PORT")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *name == "" || *workload == "" || *port == 0 {
		fmt.Fprintln(os.Stderr, "error: --name, --workload, and --port are required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.CreateService(c, &gen.CreateServiceRequest{
		Name:         *name,
		WorkloadName: *workload,
		TargetPort:   uint32(*port),
	})
	if err != nil {
		die("create service: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	fmt.Printf("created service %s (system port %d)\n", resp.ServiceId, resp.SystemPort)
}

func serviceDeleteCmd(server string, args []string) {
	fs := flag.NewFlagSet("service delete", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl service delete SERVICE_ID [SERVICE_ID...]")
	}
	_ = fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one SERVICE_ID is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()

	for _, id := range fs.Args() {
		c, cancel := reqCtx()
		resp, err := client.DeleteService(c, &gen.DeleteServiceRequest{ServiceId: id})
		cancel()
		if err != nil {
			die("delete service %s: %v", id, err)
		}
		if !resp.Accepted {
			die("rejected: %s", resp.Reason)
		}
		fmt.Printf("deleted %s\n", id)
	}
}
