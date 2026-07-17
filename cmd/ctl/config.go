package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func configCmd(server string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ctl config <list|create|update|delete|get> [flags]")
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		configListCmd(server, rest)
	case "create":
		configCreateCmd(server, rest)
	case "update":
		configUpdateCmd(server, rest)
	case "delete":
		configDeleteCmd(server, rest)
	case "get":
		configGetCmd(server, rest)
	default:
		fmt.Fprintf(os.Stderr, "ctl config: unknown subcommand %q\n", sub)
		os.Exit(1)
	}
}

func configListCmd(server string, args []string) {
	fs := flag.NewFlagSet("config list", flag.ExitOnError)
	_ = fs.Parse(args)

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.ListConfigValues(c, &gen.ListConfigValuesRequest{})
	if err != nil {
		die("list config values: %v", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tVALUE\tAGE")
	for _, cv := range resp.ConfigValues {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", cv.Id, cv.Name, cv.Value, fmtAge(cv.CreatedAt))
	}
	_ = tw.Flush()
}

func configCreateCmd(server string, args []string) {
	fs := flag.NewFlagSet("config create", flag.ExitOnError)
	name := fs.String("name", "", "unique config value name (required)")
	value := fs.String("value", "", "plaintext config value")
	valueFile := fs.String("value-file", "", "read config value from file (alternative to --value)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl config create --name NAME (--value VALUE | --value-file FILE)")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "error: --name is required")
		fs.Usage()
		os.Exit(1)
	}

	val := *value
	if *valueFile != "" {
		data, err := os.ReadFile(*valueFile)
		if err != nil {
			die("read value file: %v", err)
		}
		val = string(data)
	}
	if val == "" {
		fmt.Fprintln(os.Stderr, "error: --value or --value-file is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.CreateConfigValue(c, &gen.CreateConfigValueRequest{Name: *name, Value: val})
	if err != nil {
		die("create config value: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	fmt.Printf("created config value %s (%s)\n", *name, resp.ConfigId)
}

func configUpdateCmd(server string, args []string) {
	fs := flag.NewFlagSet("config update", flag.ExitOnError)
	value := fs.String("value", "", "new plaintext config value")
	valueFile := fs.String("value-file", "", "read new config value from file (alternative to --value)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl config update NAME (--value VALUE | --value-file FILE)")
		fs.PrintDefaults()
	}

	// NAME is positional and comes before the flags, but Go's flag package
	// stops parsing at the first non-flag argument — so pull NAME out first
	// and parse the rest as flags, same trailing-arg problem run.go works
	// around for -p/-e/-v/-l placed after IMAGE.
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: exactly one NAME is required")
		fs.Usage()
		os.Exit(1)
	}
	name := args[0]
	_ = fs.Parse(args[1:])

	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "error: unexpected extra arguments")
		fs.Usage()
		os.Exit(1)
	}

	val := *value
	if *valueFile != "" {
		data, err := os.ReadFile(*valueFile)
		if err != nil {
			die("read value file: %v", err)
		}
		val = string(data)
	}
	if val == "" {
		fmt.Fprintln(os.Stderr, "error: --value or --value-file is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.UpdateConfigValue(c, &gen.UpdateConfigValueRequest{Name: name, Value: val})
	if err != nil {
		die("update config value: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	fmt.Printf("updated config value %s\n", name)
}

func configDeleteCmd(server string, args []string) {
	fs := flag.NewFlagSet("config delete", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl config delete CONFIG_ID [CONFIG_ID...]")
	}
	_ = fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one CONFIG_ID is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()

	for _, id := range fs.Args() {
		c, cancel := reqCtx()
		resp, err := client.DeleteConfigValue(c, &gen.DeleteConfigValueRequest{ConfigId: id})
		cancel()
		if err != nil {
			die("delete config value %s: %v", id, err)
		}
		if !resp.Accepted {
			die("rejected: %s", resp.Reason)
		}
		fmt.Printf("deleted %s\n", id)
	}
}

func configGetCmd(server string, args []string) {
	fs := flag.NewFlagSet("config get", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl config get NAME")
	}
	_ = fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "error: exactly one NAME is required")
		fs.Usage()
		os.Exit(1)
	}
	name := fs.Arg(0)

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.ListConfigValues(c, &gen.ListConfigValuesRequest{})
	if err != nil {
		die("list config values: %v", err)
	}
	for _, cv := range resp.ConfigValues {
		if cv.Name == name {
			fmt.Println(cv.Value)
			return
		}
	}
	die("config value %q not found", name)
}
