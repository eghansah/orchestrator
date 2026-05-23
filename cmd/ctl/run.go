package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	gen "github.com/eghansah/orchestrator/internal/grpc/gen"
)

// stringList implements flag.Value for repeatable flags (-e KEY=VAL -e KEY2=VAL2).
type stringList []string

func (sl *stringList) String() string        { return strings.Join(*sl, ",") }
func (sl *stringList) Set(v string) error    { *sl = append(*sl, v); return nil }

func runContainerCmd(server string, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	name := fs.String("name", "", "container name (required)")
	var ports, envs, volumes, labels stringList
	fs.Var(&ports, "p", "port mapping `HOST:CONTAINER[/PROTO]` (repeatable)")
	fs.Var(&envs, "e", "environment variable `KEY=VALUE` (repeatable)")
	fs.Var(&volumes, "v", "volume `SOURCE:TARGET[:ro]` (repeatable)")
	fs.Var(&labels, "l", "label `KEY=VALUE` (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl run --name NAME [flags] IMAGE [COMMAND...]")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "error: --name is required")
		fs.Usage()
		os.Exit(1)
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: IMAGE is required")
		fs.Usage()
		os.Exit(1)
	}
	image, command := fs.Arg(0), fs.Args()[1:]

	portMappings, err := parsePorts(ports)
	if err != nil {
		die("parse ports: %v", err)
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.SubmitContainer(c, &gen.SubmitContainerRequest{
		Spec: &gen.ContainerSpec{
			Name:    *name,
			Image:   image,
			Command: command,
			Env:     []string(envs),
			Ports:   portMappings,
			Volumes: parseVolumes(volumes),
			Labels:  parseLabels(labels),
		},
	})
	if err != nil {
		die("submit container: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	fmt.Println(resp.WorkloadId)
}

func runStackCmd(server string, args []string) {
	fs := flag.NewFlagSet("stack", flag.ExitOnError)
	name := fs.String("name", "", "stack name (required)")
	file := fs.String("f", "", "path to compose file (required)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl stack --name NAME -f FILE")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	if *name == "" || *file == "" {
		fmt.Fprintln(os.Stderr, "error: --name and -f are required")
		fs.Usage()
		os.Exit(1)
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		die("read %s: %v", *file, err)
	}

	conn, client := dial(server)
	defer conn.Close()
	c, cancel := reqCtx()
	defer cancel()

	resp, err := client.SubmitStack(c, &gen.SubmitStackRequest{
		Spec: &gen.ComposeStackSpec{
			Name:        *name,
			ComposeYaml: string(data),
		},
	})
	if err != nil {
		die("submit stack: %v", err)
	}
	if !resp.Accepted {
		die("rejected: %s", resp.Reason)
	}
	fmt.Println(resp.WorkloadId)
}

// ── Parsers ───────────────────────────────────────────────────────────────────

func parsePorts(ports []string) ([]*gen.PortMapping, error) {
	out := make([]*gen.PortMapping, 0, len(ports))
	for _, raw := range ports {
		proto := "tcp"
		p := raw
		if i := strings.LastIndex(p, "/"); i >= 0 {
			proto = p[i+1:]
			p = p[:i]
		}
		halves := strings.SplitN(p, ":", 2)
		if len(halves) != 2 {
			return nil, fmt.Errorf("invalid port mapping %q — want HOST:CONTAINER[/PROTO]", raw)
		}
		host, err := strconv.ParseUint(halves[0], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bad host port in %q: %v", raw, err)
		}
		ctr, err := strconv.ParseUint(halves[1], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bad container port in %q: %v", raw, err)
		}
		out = append(out, &gen.PortMapping{
			HostPort:      uint32(host),
			ContainerPort: uint32(ctr),
			Protocol:      proto,
		})
	}
	return out, nil
}

func parseVolumes(volumes []string) []*gen.VolumeMount {
	out := make([]*gen.VolumeMount, 0, len(volumes))
	for _, raw := range volumes {
		v, ro := raw, false
		if strings.HasSuffix(v, ":ro") {
			ro = true
			v = v[:len(v)-3]
		}
		halves := strings.SplitN(v, ":", 2)
		if len(halves) != 2 {
			die("invalid volume %q — want SOURCE:TARGET[:ro]", raw)
		}
		out = append(out, &gen.VolumeMount{Source: halves[0], Target: halves[1], ReadOnly: ro})
	}
	return out
}

func parseLabels(labels []string) map[string]string {
	m := make(map[string]string, len(labels))
	for _, kv := range labels {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
