// Admin subcommands for probe configuration: targets, probes, mesh groups.
// All run against the database directly (docker compose exec server …) and
// fail loudly: unresolvable names name the missing row, and sites are never
// auto-created here — a typo'd --site must be an error, unlike token create.
package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
	"github.com/devalexllc/lighthouse/internal/server/config"
	"github.com/devalexllc/lighthouse/internal/server/store"
)

// probeTypeNames maps CLI names to wire enum values. dns/icmp/traceroute
// join in M4.
var probeTypeNames = map[string]pb.ProbeType{
	"tcp":  pb.ProbeType_PROBE_TYPE_TCP,
	"tls":  pb.ProbeType_PROBE_TYPE_TLS,
	"http": pb.ProbeType_PROBE_TYPE_HTTP,
}

func parseProbeType(name string) (pb.ProbeType, error) {
	t, ok := probeTypeNames[name]
	if !ok {
		accepted := make([]string, 0, len(probeTypeNames))
		for k := range probeTypeNames {
			accepted = append(accepted, k)
		}
		return 0, fmt.Errorf("unknown probe type %q (accepted: %s)", name, strings.Join(accepted, ", "))
	}
	return t, nil
}

func probeTypeName(t int16) string {
	for name, v := range probeTypeNames {
		if int16(v) == t {
			return name
		}
	}
	return fmt.Sprintf("type-%d", t)
}

// paramsFlag collects repeated --param k=v flags.
type paramsFlag map[string]string

func (p paramsFlag) String() string { return "" }

func (p paramsFlag) Set(kv string) error {
	k, v, found := strings.Cut(kv, "=")
	if !found || k == "" {
		return fmt.Errorf("--param must be key=value, got %q", kv)
	}
	p[k] = v
	return nil
}

// adminStore opens the store the way cmdToken does.
func adminStore(cfg config.Config) (*store.Store, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	st, err := store.Connect(ctx, cfg.DB.URL, cfg.DB.ConnectTimeout)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	return st, ctx, cancel, nil
}

func cmdTarget(args []string) error {
	const use = "usage: lighthouse-server target add|list|rm ..."
	if len(args) < 1 {
		return fmt.Errorf(use)
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("target add", flag.ExitOnError)
		name := fs.String("name", "", "unique target name")
		address := fs.String("address", "", "host or IP to probe")
		port := fs.Int("port", 0, "port for tcp/tls probes")
		url := fs.String("url", "", "full URL for http probes")
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		if *address == "" && *url == "" {
			return fmt.Errorf("--address or --url is required")
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		id, err := st.UpsertExternalTarget(ctx, *name, *address, int32(*port), *url)
		if err != nil {
			return err
		}
		fmt.Printf("target %q ready (%s)\n", *name, id)
		return nil

	case "list":
		fs := flag.NewFlagSet("target list", flag.ExitOnError)
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		targets, err := st.ListTargets(ctx)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			fmt.Println("no targets")
			return nil
		}
		fmt.Printf("%-36s  %-8s  %-24s  %s\n", "ID", "KIND", "NAME", "ADDRESS")
		for _, t := range targets {
			addr := t.Address
			if t.Port > 0 {
				addr = fmt.Sprintf("%s:%d", t.Address, t.Port)
			}
			if t.URL != "" {
				addr = t.URL
			}
			fmt.Printf("%-36s  %-8s  %-24s  %s\n", t.ID, t.Kind, t.Name, addr)
		}
		return nil

	case "rm":
		fs := flag.NewFlagSet("target rm", flag.ExitOnError)
		name := fs.String("name", "", "target name to remove")
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		if err := st.DeleteTarget(ctx, *name); err != nil {
			return err
		}
		fmt.Printf("target %q removed\n", *name)
		return nil
	}
	return fmt.Errorf("unknown target subcommand %q\n%s", args[0], use)
}

func cmdProbe(args []string) error {
	const use = "usage: lighthouse-server probe add|list|rm ..."
	if len(args) < 1 {
		return fmt.Errorf(use)
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("probe add", flag.ExitOnError)
		site := fs.String("site", "", "site whose agents run the probe (with --target)")
		target := fs.String("target", "", "target name to probe (with --site)")
		mesh := fs.String("mesh", "", "mesh group to expand over ordered site pairs (instead of --site/--target)")
		typeName := fs.String("type", "", "probe type: tcp, tls, http")
		interval := fs.Duration("interval", 30*time.Second, "run interval")
		timeout := fs.Duration("timeout", 5*time.Second, "per-run timeout")
		params := paramsFlag{}
		fs.Var(params, "param", "type-specific key=value (repeatable), e.g. http.expect_status=200, port=5432")
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}

		probeType, err := parseProbeType(*typeName)
		if err != nil {
			return err
		}
		meshMode := *mesh != ""
		directMode := *site != "" || *target != ""
		if meshMode == directMode {
			return fmt.Errorf("exactly one of --mesh or --site+--target is required")
		}
		if directMode && (*site == "" || *target == "") {
			return fmt.Errorf("--site and --target are both required for a direct probe")
		}
		if *interval <= 0 || *timeout <= 0 {
			return fmt.Errorf("--interval and --timeout must be positive")
		}
		if *timeout >= *interval {
			return fmt.Errorf("--timeout (%s) must be shorter than --interval (%s)", *timeout, *interval)
		}

		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		ps := store.ProbeSettings{
			ProbeType: int16(probeType),
			Interval:  *interval,
			Timeout:   *timeout,
			Params:    params,
		}
		var id uuid.UUID
		if meshMode {
			id, err = st.AddMeshProbe(ctx, *mesh, ps)
		} else {
			id, err = st.AddDirectProbe(ctx, *site, *target, ps)
		}
		if err != nil {
			return err
		}
		fmt.Printf("probe %s added\n", id)
		return nil

	case "list":
		fs := flag.NewFlagSet("probe list", flag.ExitOnError)
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		probes, err := st.ListProbeConfigs(ctx)
		if err != nil {
			return err
		}
		if len(probes) == 0 {
			fmt.Println("no probes")
			return nil
		}
		fmt.Printf("%-36s  %-5s  %-28s  %-9s  %-8s  %s\n", "ID", "TYPE", "ASSIGNMENT", "INTERVAL", "TIMEOUT", "ENABLED")
		for _, p := range probes {
			assignment := fmt.Sprintf("mesh:%s", p.Mesh)
			if p.Mesh == "" {
				assignment = fmt.Sprintf("%s -> %s", p.Site, p.Target)
			}
			fmt.Printf("%-36s  %-5s  %-28s  %-9s  %-8s  %v\n",
				p.ID, probeTypeName(p.ProbeType), assignment, p.Interval, p.Timeout, p.Enabled)
		}
		return nil

	case "rm":
		fs := flag.NewFlagSet("probe rm", flag.ExitOnError)
		idStr := fs.String("id", "", "probe config id (from probe list)")
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		id, err := uuid.Parse(*idStr)
		if err != nil {
			return fmt.Errorf("--id must be a probe config UUID: %w", err)
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		if err := st.DeleteProbeConfig(ctx, id); err != nil {
			return err
		}
		fmt.Printf("probe %s removed\n", id)
		return nil
	}
	return fmt.Errorf("unknown probe subcommand %q\n%s", args[0], use)
}

func cmdMesh(args []string) error {
	const use = "usage: lighthouse-server mesh create|add|rm|list ..."
	if len(args) < 1 {
		return fmt.Errorf(use)
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("mesh create", flag.ExitOnError)
		name := fs.String("name", "", "mesh group name")
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		id, err := st.UpsertMeshGroup(ctx, *name)
		if err != nil {
			return err
		}
		fmt.Printf("mesh %q ready (%s)\n", *name, id)
		return nil

	case "add", "rm":
		fs := flag.NewFlagSet("mesh "+args[0], flag.ExitOnError)
		name := fs.String("name", "", "mesh group name")
		site := fs.String("site", "", "site to add/remove")
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		if *name == "" || *site == "" {
			return fmt.Errorf("--name and --site are required")
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		if args[0] == "add" {
			if err := st.AddMeshMember(ctx, *name, *site); err != nil {
				return err
			}
			fmt.Printf("site %q added to mesh %q\n", *site, *name)
		} else {
			if err := st.RemoveMeshMember(ctx, *name, *site); err != nil {
				return err
			}
			fmt.Printf("site %q removed from mesh %q\n", *site, *name)
		}
		return nil

	case "list":
		fs := flag.NewFlagSet("mesh list", flag.ExitOnError)
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		meshes, err := st.ListMeshGroups(ctx)
		if err != nil {
			return err
		}
		if len(meshes) == 0 {
			fmt.Println("no mesh groups")
			return nil
		}
		for _, m := range meshes {
			fmt.Printf("%-16s  sites: %s\n", m.Name, strings.Join(m.Sites, ", "))
		}
		return nil
	}
	return fmt.Errorf("unknown mesh subcommand %q\n%s", args[0], use)
}
