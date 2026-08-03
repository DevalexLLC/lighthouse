// lighthouse-agent is the Lighthouse site agent: a single static binary that
// probes peers and endpoints, spools results to disk, and reports to the
// control plane over mTLS.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/devalexllc/lighthouse/internal/agent/config"
	"github.com/devalexllc/lighthouse/internal/agent/enroll"
	"github.com/devalexllc/lighthouse/internal/agent/probes"
	"github.com/devalexllc/lighthouse/internal/agent/scheduler"
	"github.com/devalexllc/lighthouse/internal/agent/spool"
	"github.com/devalexllc/lighthouse/internal/agent/uplink"
	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
	"github.com/devalexllc/lighthouse/internal/version"
)

const usage = `lighthouse-agent — Lighthouse site agent

Usage:
  lighthouse-agent run       --config <file>         run the agent
  lighthouse-agent enroll    --config <file> --token <join-token>
                             (--ca-cert <file> | --fingerprint sha256:<hex>)
                             [--probe-address <host>]
                                                     enroll with the control plane
  lighthouse-agent selfcheck --config <file>         verify probe capabilities
  lighthouse-agent version                           print version and exit
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "enroll":
		err = cmdEnroll(os.Args[2:])
	case "selfcheck":
		err = cmdSelfcheck(os.Args[2:])
	case "version", "--version":
		fmt.Println("lighthouse-agent", version.String())
		return
	case "help", "--help", "-h":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig(fs *flag.FlagSet, args []string) (config.Config, error) {
	cfgPath := fs.String("config", "/etc/lighthouse/agent.yaml", "path to agent config file")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	return config.Load(*cfgPath)
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	token := fs.String("token", "", "one-time join token")
	caCert := fs.String("ca-cert", "", "path to the control plane CA certificate")
	fingerprint := fs.String("fingerprint", "", "pinned CA fingerprint (sha256:<hex>)")
	probeAddr := fs.String("probe-address", "", "address peers should probe (required behind NAT)")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	return enroll.Run(context.Background(), cfg, enroll.Options{
		Token:        *token,
		CACertFile:   *caCert,
		Fingerprint:  *fingerprint,
		ProbeAddress: *probeAddr,
	})
}

// cmdSelfcheck verifies the capabilities the probers need (ICMP socket
// modes, traceroute's raw socket, spool writability) and exits non-zero if
// any fatal check fails. Config load itself is the first check: a bad file
// fails before anything else runs.
func cmdSelfcheck(args []string) error {
	fs := flag.NewFlagSet("selfcheck", flag.ExitOnError)
	cfg, err := loadConfig(fs, args)
	if err != nil {
		fmt.Printf("%-18s %-5s %v\n", "config", "FAIL", err)
		return fmt.Errorf("selfcheck failed")
	}
	fmt.Printf("%-18s %-5s %s\n", "config", "ok", fs.Lookup("config").Value.String())

	failed := false
	for _, c := range probes.SelfCheck(cfg.StateDir) {
		status := "ok"
		if !c.OK {
			status = "FAIL"
			if c.Fatal {
				failed = true
			}
		}
		fmt.Printf("%-18s %-5s %s\n", c.Name, status, c.Detail)
	}
	if failed {
		return fmt.Errorf("selfcheck failed")
	}
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	setupLogging(cfg.Log.Level)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	up, err := uplink.New(cfg)
	if err != nil {
		return err
	}
	defer up.Close()

	slog.Info("lighthouse-agent starting", "version", version.String(), "server", cfg.Server.Address)

	// Spool-first single path: every result is written to disk, the pusher
	// drains from there. A spool that cannot be opened is fatal — running
	// without it would silently lose results across outages.
	sp, err := spool.Open(filepath.Join(cfg.StateDir, "spool"), cfg.Spool.MaxBytes, cfg.Spool.MaxAge)
	if err != nil {
		return err
	}
	defer sp.Close()

	sched := scheduler.New(probes.DefaultRegistry(), func(res *pb.ProbeResult) {
		if err := sp.Append(res); err != nil {
			slog.Error("spool append failed", "probe", res.GetProbeId(), "err", err)
		}
	})
	defer sched.Stop()
	up.OnSnapshot = sched.Apply

	go uplink.NewPusher(up, sp).Run(ctx)
	go up.NewRenewer().Run(ctx)
	return up.Run(ctx)
}

func setupLogging(level string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
}
