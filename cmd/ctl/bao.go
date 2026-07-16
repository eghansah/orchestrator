package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

func baoCmd(server string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ctl bao <status|unseal> [flags]")
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "status":
		baoStatusCmd(server, rest)
	case "unseal":
		baoUnsealCmd(server, rest)
	default:
		fmt.Fprintf(os.Stderr, "ctl bao: unknown subcommand %q\n", sub)
		os.Exit(1)
	}
}

func baoStatusCmd(server string, args []string) {
	fs := flag.NewFlagSet("bao status", flag.ExitOnError)
	_ = fs.Parse(args)

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.GetBaoSealStatus(c, &gen.GetBaoSealStatusRequest{})
	if err != nil {
		die("get seal status: %v", err)
	}
	if !resp.Configured {
		fmt.Println("OpenBao is not configured — set an address via the Secrets page or ctl.")
		return
	}
	if !resp.Reachable {
		fmt.Printf("Configured: yes\nReachable:  no (%s)\n", resp.Error)
		return
	}
	fmt.Printf("Configured:  yes\n")
	fmt.Printf("Initialized: %t\n", resp.Initialized)
	fmt.Printf("Sealed:      %t\n", resp.Sealed)
	if resp.Threshold > 0 {
		fmt.Printf("Progress:    %d/%d key shares (of %d total)\n", resp.Progress, resp.Threshold, resp.Shares)
	}
}

func baoUnsealCmd(server string, args []string) {
	fs := flag.NewFlagSet("bao unseal", flag.ExitOnError)
	key := fs.String("key", "", "one unseal key share (omit to read a line from stdin)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl bao unseal --key KEY")
		fmt.Fprintln(os.Stderr, "       echo KEY | ctl bao unseal")
		fmt.Fprintln(os.Stderr, "Submits one Shamir unseal key share. Run once per key share until sealed=false.")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	k := *key
	if k == "" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			die("read key from stdin: %v", err)
		}
		k = strings.TrimSpace(line)
	}
	if k == "" {
		fmt.Fprintln(os.Stderr, "error: --key or a key on stdin is required")
		fs.Usage()
		os.Exit(1)
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.UnsealBao(c, &gen.UnsealBaoRequest{Key: k})
	if err != nil {
		die("unseal: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	if resp.Sealed {
		fmt.Printf("key accepted — progress %d/%d, still sealed\n", resp.Progress, resp.Threshold)
	} else {
		fmt.Println("unsealed")
	}
}
