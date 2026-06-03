package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func secretCmd(server string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ctl secret <list|create|delete> [flags]")
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		secretListCmd(server, rest)
	case "create":
		secretCreateCmd(server, rest)
	case "delete":
		secretDeleteCmd(server, rest)
	default:
		fmt.Fprintf(os.Stderr, "ctl secret: unknown subcommand %q\n", sub)
		os.Exit(1)
	}
}

func secretListCmd(server string, args []string) {
	fs := flag.NewFlagSet("secret list", flag.ExitOnError)
	_ = fs.Parse(args)

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.ListSecrets(c, &gen.ListSecretsRequest{})
	if err != nil {
		die("list secrets: %v", err)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tAGE")
	for _, s := range resp.Secrets {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Id, s.Name, fmtAge(s.CreatedAt))
	}
	_ = tw.Flush()
}

func secretCreateCmd(server string, args []string) {
	fs := flag.NewFlagSet("secret create", flag.ExitOnError)
	name := fs.String("name", "", "unique secret name (required)")
	value := fs.String("value", "", "plaintext secret value")
	valueFile := fs.String("value-file", "", "read secret value from file (alternative to --value)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl secret create --name NAME (--value VALUE | --value-file FILE)")
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

	resp, err := client.CreateSecret(c, &gen.CreateSecretRequest{Name: *name, Value: val})
	if err != nil {
		die("create secret: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	fmt.Printf("created secret %s (%s)\n", *name, resp.SecretId)
}

func secretDeleteCmd(server string, args []string) {
	fs := flag.NewFlagSet("secret delete", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl secret delete SECRET_ID [SECRET_ID...]")
	}
	_ = fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: at least one SECRET_ID is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()

	for _, id := range fs.Args() {
		c, cancel := reqCtx()
		resp, err := client.DeleteSecret(c, &gen.DeleteSecretRequest{SecretId: id})
		cancel()
		if err != nil {
			die("delete secret %s: %v", id, err)
		}
		if !resp.Accepted {
			die("rejected: %s", resp.Reason)
		}
		fmt.Printf("deleted %s\n", id)
	}
}
