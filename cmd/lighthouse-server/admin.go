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

// probeTypeNames maps CLI names to wire enum values.
var probeTypeNames = map[string]pb.ProbeType{
	"icmp":       pb.ProbeType_PROBE_TYPE_ICMP,
	"tcp":        pb.ProbeType_PROBE_TYPE_TCP,
	"tls":        pb.ProbeType_PROBE_TYPE_TLS,
	"http":       pb.ProbeType_PROBE_TYPE_HTTP,
	"dns":        pb.ProbeType_PROBE_TYPE_DNS,
	"traceroute": pb.ProbeType_PROBE_TYPE_TRACEROUTE,
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

// validateTrain rejects train settings that cannot fit inside the per-run
// timeout: the agent budgets the whole train within spec.timeout, so a
// train longer than the timeout would silently lose its tail.
func validateTrain(count int, spacing, timeout time.Duration) error {
	if count < 0 || spacing < 0 {
		return fmt.Errorf("--train-count and --train-spacing must not be negative")
	}
	if count == 0 {
		// Snapshot building only forwards spacing alongside a positive
		// count; accepting spacing alone would silently no-op on the agent.
		if spacing > 0 {
			return fmt.Errorf("--train-spacing requires --train-count")
		}
		return nil
	}
	effSpacing := spacing
	if effSpacing == 0 {
		effSpacing = 200 * time.Millisecond // prober default
	}
	if trainLen := time.Duration(count) * effSpacing; trainLen >= timeout {
		return fmt.Errorf("train of %d × %s (%s) must fit inside --timeout (%s)",
			count, effSpacing, trainLen, timeout)
	}
	return nil
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

// validateCoords rejects out-of-range map positions.
func validateCoords(lat, lon float64) error {
	if lat < -90 || lat > 90 {
		return fmt.Errorf("--lat must be between -90 and 90, got %g", lat)
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("--lon must be between -180 and 180, got %g", lon)
	}
	return nil
}

func cmdSite(args []string) error {
	const use = "usage: lighthouse-server site list|set ..."
	if len(args) < 1 {
		return fmt.Errorf(use)
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("site list", flag.ExitOnError)
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
		sites, err := st.ListSites(ctx)
		if err != nil {
			return err
		}
		if len(sites) == 0 {
			fmt.Println("no sites")
			return nil
		}
		fmt.Printf("%-16s  %-20s  %-9s  %-9s  %s\n", "NAME", "DISPLAY", "LAT", "LON", "LOCATION")
		for _, s := range sites {
			lat, lon := "—", "—"
			if s.Latitude != nil {
				lat = fmt.Sprintf("%.4f", *s.Latitude)
				lon = fmt.Sprintf("%.4f", *s.Longitude)
			}
			fmt.Printf("%-16s  %-20s  %-9s  %-9s  %s\n", s.Name, s.DisplayName, lat, lon, s.Location)
		}
		return nil

	case "set":
		fs := flag.NewFlagSet("site set", flag.ExitOnError)
		name := fs.String("name", "", "site name (must already exist)")
		lat := fs.Float64("lat", 0, "latitude in degrees (-90..90), with --lon")
		lon := fs.Float64("lon", 0, "longitude in degrees (-180..180), with --lat")
		display := fs.String("display-name", "", "human-friendly site name")
		location := fs.String("location", "", "free-text location label")
		cfg, err := loadConfig(fs, args[1:])
		if err != nil {
			return err
		}
		// 0,0 is a real coordinate (off Ghana), so presence is tracked per
		// flag rather than compared against the default value.
		set := map[string]bool{}
		fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		if set["lat"] != set["lon"] {
			return fmt.Errorf("--lat and --lon must be given together")
		}
		if !set["lat"] && !set["display-name"] && !set["location"] {
			return fmt.Errorf("nothing to set: give --lat/--lon, --display-name, or --location")
		}
		var up store.SiteUpdate
		if set["lat"] {
			if err := validateCoords(*lat, *lon); err != nil {
				return err
			}
			up.Latitude, up.Longitude = lat, lon
		}
		if set["display-name"] {
			up.DisplayName = display
		}
		if set["location"] {
			up.Location = location
		}
		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		if err := st.UpdateSite(ctx, *name, up); err != nil {
			return err
		}
		fmt.Printf("site %q updated\n", *name)
		return nil
	}
	return fmt.Errorf("unknown site subcommand %q\n%s", args[0], use)
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
		typeName := fs.String("type", "", "probe type: icmp, tcp, tls, http, dns, traceroute")
		interval := fs.Duration("interval", 30*time.Second, "run interval")
		timeout := fs.Duration("timeout", 5*time.Second, "per-run timeout")
		trainCount := fs.Int("train-count", 0, "packets per run for train probes (icmp); 0 = prober default (10)")
		trainSpacing := fs.Duration("train-spacing", 0, "gap between train packets; 0 = prober default (200ms)")
		params := paramsFlag{}
		fs.Var(params, "param", "type-specific key=value (repeatable), e.g. http.expect_status=200, dns.qname=example.org, port=5432")
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
		if err := validateTrain(*trainCount, *trainSpacing, *timeout); err != nil {
			return err
		}

		st, ctx, cancel, err := adminStore(cfg)
		if err != nil {
			return err
		}
		defer cancel()
		defer st.Close()
		ps := store.ProbeSettings{
			ProbeType:    int16(probeType),
			Interval:     *interval,
			Timeout:      *timeout,
			TrainCount:   int32(*trainCount),
			TrainSpacing: *trainSpacing,
			Params:       params,
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
