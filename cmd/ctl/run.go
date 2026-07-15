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
	replicas := fs.Int("replicas", 1, "number of instances to run across nodes")
	var ports, envs, volumes, labels, secrets stringList
	fs.Var(&ports, "p", "container port to expose `PORT[/PROTO]` (repeatable); host port is auto-assigned")
	fs.Var(&envs, "e", "environment variable `KEY=VALUE` (repeatable)")
	fs.Var(&volumes, "v", "volume `SOURCE:TARGET[:ro]` (repeatable)")
	fs.Var(&labels, "l", "label `KEY=VALUE` (repeatable)")
	fs.Var(&secrets, "secret", "inject secret as env var `ENV_VAR=secret_name` (repeatable)")
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
	image := fs.Arg(0)

	// Go's flag package stops at the first non-flag arg, so flags placed
	// after IMAGE (e.g. `ctl run --name foo nginx -p 80`) end up in the
	// trailing args.  Extract any -p/-e/-v/-l flags that landed there.
	rest := fs.Args()[1:]
	command := make([]string, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		switch {
		case arg == "-p" || arg == "-e" || arg == "-v" || arg == "-l":
			if i+1 < len(rest) {
				switch arg {
				case "-p":
					ports = append(ports, rest[i+1])
				case "-e":
					envs = append(envs, rest[i+1])
				case "-v":
					volumes = append(volumes, rest[i+1])
				case "-l":
					labels = append(labels, rest[i+1])
				}
				i++ // skip the value
			}
		default:
			command = append(command, arg)
		}
	}

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
			Name:       *name,
			Image:      image,
			Command:    command,
			Env:        []string(envs),
			Ports:      portMappings,
			Volumes:    parseVolumes(volumes),
			Labels:     parseLabels(labels),
			SecretRefs: parseSecretRefs(secrets),
			Replicas:   int32(*replicas),
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
	var secrets stringList
	fs.Var(&secrets, "secret", "inject secret as env var `ENV_VAR=secret_name` (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: ctl stack --name NAME -f FILE [--secret ENV_VAR=secret_name]")
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
			SecretRefs:  parseSecretRefs(secrets),
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
		// Accept both CONTAINER and HOST:CONTAINER (host port is ignored by the
		// orchestrator — it auto-assigns one from the port pool).
		halves := strings.SplitN(p, ":", 2)
		var ctrStr string
		if len(halves) == 2 {
			ctrStr = halves[1]
		} else {
			ctrStr = halves[0]
		}
		ctr, err := strconv.ParseUint(ctrStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("bad container port in %q: %v", raw, err)
		}
		out = append(out, &gen.PortMapping{
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

// parseSecretRefs converts "ENV_VAR=secret_name" entries into a map.
func parseSecretRefs(refs []string) map[string]string {
	if len(refs) == 0 {
		return nil
	}
	m := make(map[string]string, len(refs))
	for _, kv := range refs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		} else {
			die("invalid --secret %q: expected ENV_VAR=secret_name", kv)
		}
	}
	return m
}
