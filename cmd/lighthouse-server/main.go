// lighthouse-server is the Lighthouse control plane: gRPC API for agents,
// REST API + dashboard for humans, built-in CA, and TimescaleDB storage.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/devalexllc/lighthouse/internal/server"
	"github.com/devalexllc/lighthouse/internal/server/ca"
	"github.com/devalexllc/lighthouse/internal/server/config"
	"github.com/devalexllc/lighthouse/internal/server/migrate"
	"github.com/devalexllc/lighthouse/internal/server/seed"
	"github.com/devalexllc/lighthouse/internal/server/store"
	"github.com/devalexllc/lighthouse/internal/version"
)

const usage = `lighthouse-server — Lighthouse control plane

Usage:
  lighthouse-server serve   --config <file>          run the control plane
  lighthouse-server migrate --config <file> [--timeout 30m]
                                                     apply database migrations
  lighthouse-server ca init --config <file> [--if-missing]
                                                     create the built-in CA
  lighthouse-server token create --config <file> --site <name> [--ttl 24h] [--quiet]
                                                     issue an agent join token
  lighthouse-server target add|list|rm --config <file> ...
                                                     manage external probe targets
  lighthouse-server probe add|list|rm --config <file> ...
                                                     manage probe assignments
  lighthouse-server mesh create|add|rm|list --config <file> ...
                                                     manage full-mesh site groups
  lighthouse-server user add --config <file> --username <name> [--admin]
                                                     create a dashboard user
  lighthouse-server seed --config <file> [--days 90]
                                                     load synthetic probe history (dev/gate)
  lighthouse-server version                          print version and exit
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "migrate":
		err = cmdMigrate(os.Args[2:])
	case "ca":
		err = cmdCA(os.Args[2:])
	case "token":
		err = cmdToken(os.Args[2:])
	case "target":
		err = cmdTarget(os.Args[2:])
	case "probe":
		err = cmdProbe(os.Args[2:])
	case "mesh":
		err = cmdMesh(os.Args[2:])
	case "user":
		err = cmdUser(os.Args[2:])
	case "seed":
		err = cmdSeed(os.Args[2:])
	case "version", "--version":
		fmt.Println("lighthouse-server", version.String())
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
	cfgPath := fs.String("config", "/etc/lighthouse/server.yaml", "path to server config file")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	return config.Load(*cfgPath)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	setupLogging(cfg.Log.Level)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	slog.Info("lighthouse-server starting", "version", version.String())
	return server.Run(ctx, cfg)
}

func cmdMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	// The M5 cagg backfills (0008/0009) recompute up to 400 d of history on
	// upgrade; the default is generous, and a huge deployment can raise it
	// (a single over-deadline backfill would otherwise restart from scratch
	// on every retry, never completing).
	timeout := fs.Duration("timeout", 30*time.Minute, "overall migration deadline")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.DB.URL)
	if err != nil {
		return fmt.Errorf("db unreachable at configured db.url: %w", err)
	}
	defer conn.Close(ctx)
	return migrate.Apply(ctx, conn)
}

func cmdCA(args []string) error {
	if len(args) < 1 || args[0] != "init" {
		return fmt.Errorf("usage: lighthouse-server ca init --config <file> [--if-missing]")
	}
	fs := flag.NewFlagSet("ca init", flag.ExitOnError)
	ifMissing := fs.Bool("if-missing", false, "succeed as a no-op when a CA already exists")
	cfg, err := loadConfig(fs, args[1:])
	if err != nil {
		return err
	}
	if err := ca.Init(cfg.CA.Dir, *ifMissing); err != nil {
		return err
	}
	authority, err := ca.Load(cfg.CA.Dir)
	if err != nil {
		return err
	}
	fmt.Printf("CA ready in %s\nfingerprint sha256:%s\n", cfg.CA.Dir, authority.Fingerprint())
	return nil
}

func cmdToken(args []string) error {
	if len(args) < 1 || args[0] != "create" {
		return fmt.Errorf("usage: lighthouse-server token create --config <file> --site <name> [--ttl 24h] [--quiet]")
	}
	fs := flag.NewFlagSet("token create", flag.ExitOnError)
	site := fs.String("site", "", "site the enrolling agent belongs to (created if missing)")
	ttl := fs.Duration("ttl", 24*time.Hour, "token validity window")
	quiet := fs.Bool("quiet", false, "print only the token (for scripting)")
	cfg, err := loadConfig(fs, args[1:])
	if err != nil {
		return err
	}
	if *site == "" {
		return fmt.Errorf("--site is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Connect(ctx, cfg.DB.URL, cfg.DB.ConnectTimeout)
	if err != nil {
		return err
	}
	defer st.Close()
	authority, err := ca.Load(cfg.CA.Dir)
	if err != nil {
		return err
	}

	siteID, err := st.EnsureSite(ctx, *site)
	if err != nil {
		return err
	}
	token, err := st.CreateJoinToken(ctx, siteID, os.Getenv("USER"), *ttl)
	if err != nil {
		return err
	}
	if *quiet {
		fmt.Println(token)
		return nil
	}
	fmt.Printf(`join token for site %q (valid %s, single use):

  %s

on the agent host:

  lighthouse-agent enroll --config /etc/lighthouse/agent.yaml \
      --token '%s' \
      --fingerprint sha256:%s
`, *site, *ttl, token, token, authority.Fingerprint())
	return nil
}

// cmdSeed loads synthetic history for the aggregate/percentile pipeline.
// Dev/gate tooling: it needs enrolled agents and runs inside the compose
// network (the DB is not host-exposed).
func cmdSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	days := fs.Int("days", 90, "days of synthetic history to load")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	if *days < 1 || *days > 400 {
		return fmt.Errorf("--days must be between 1 and 400 (daily retention)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	st, err := store.Connect(ctx, cfg.DB.URL, cfg.DB.ConnectTimeout)
	if err != nil {
		return err
	}
	defer st.Close()
	return seed.Run(ctx, st.Pool(), *days, os.Stdout)
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
