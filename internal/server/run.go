// Package server wires the control plane together: preflight checks, the two
// TLS listeners (agent gRPC with mTLS, dashboard HTTPS), and lifecycle.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"github.com/devalexllc/lighthouse/internal/server/ca"
	"github.com/devalexllc/lighthouse/internal/server/config"
	"github.com/devalexllc/lighthouse/internal/server/grpcapi"
	"github.com/devalexllc/lighthouse/internal/server/migrate"
	"github.com/devalexllc/lighthouse/internal/server/store"
	"github.com/devalexllc/lighthouse/internal/version"
)

// Run performs preflight and serves until ctx is cancelled. Every preflight
// failure is fatal and names the problem.
func Run(ctx context.Context, cfg config.Config) error {
	authority, err := ca.Load(cfg.CA.Dir)
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	st, err := store.Connect(ctx, cfg.DB.URL, cfg.DB.ConnectTimeout)
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	defer st.Close()

	// A reachable but unmigrated database must never present as healthy.
	pending, err := migrate.Pending(ctx, st.Pool())
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	if len(pending) > 0 {
		return fmt.Errorf("preflight: database schema is behind: %d pending migration(s) %v — run `lighthouse-server migrate` first",
			len(pending), pending)
	}

	dashboardCert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return fmt.Errorf("preflight: dashboard certificate (tls.cert_file/tls.key_file): %w", err)
	}

	grpcCert, err := ensureGRPCCert(authority, cfg.CA.Dir, cfg.Listen.GRPCHostname)
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	api := grpcapi.New(st, authority)

	grpcTLS := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{grpcCert},
		// Enrollment arrives with no client certificate; AgentService RPCs
		// enforce a verified cert themselves. Certs that ARE presented get
		// verified against the built-in CA here.
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  authority.Pool(),
	}
	grpcLis, err := net.Listen("tcp", cfg.Listen.GRPC)
	if err != nil {
		return fmt.Errorf("listen grpc %s: %w", cfg.Listen.GRPC, err)
	}
	grpcServer := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(grpcTLS)),
		grpc.MaxRecvMsgSize(16<<20),
		// Config streams are idle for long stretches; server pings keep
		// them alive through idle-timeout middleboxes (incl. our proxy's
		// proxy_timeout) and detect dead agents.
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    1 * time.Minute,
			Timeout: 20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             30 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	api.Register(grpcServer)

	httpLis, err := net.Listen("tcp", cfg.Listen.HTTP)
	if err != nil {
		return fmt.Errorf("listen http %s: %w", cfg.Listen.HTTP, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// M3 replaces this with the embedded SPA.
		fmt.Fprintf(w, "Lighthouse %s — dashboard coming soon\n", version.String())
	})
	httpServer := &http.Server{
		Handler:           mux,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{dashboardCert}},
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		slog.Info("gRPC listening", "addr", cfg.Listen.GRPC, "hostname", cfg.Listen.GRPCHostname)
		errCh <- grpcServer.Serve(grpcLis)
	}()
	go func() {
		slog.Info("dashboard listening", "addr", cfg.Listen.HTTP)
		err := httpServer.ServeTLS(httpLis, "", "")
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
		// Agent config streams are intentionally long-lived, so GracefulStop
		// alone would wait on them forever; bound it and force-stop.
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			slog.Info("graceful stop timed out; closing active streams")
			grpcServer.Stop()
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// ensureGRPCCert loads the auto-issued gRPC server certificate, reissuing it
// when missing, hostname-mismatched, or past 2/3 of its lifetime.
func ensureGRPCCert(authority *ca.CA, dir, hostname string) (tls.Certificate, error) {
	certPath := filepath.Join(dir, "grpc-server.crt")
	keyPath := filepath.Join(dir, "grpc-server.key")

	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		leaf := cert.Leaf
		if leaf != nil &&
			leaf.VerifyHostname(hostname) == nil &&
			time.Until(leaf.NotAfter) > ca.ServerCertLifetime/3 &&
			// Fingerprint-pinned enrollment needs the CA served in the
			// chain; reissue files from before that was included.
			len(cert.Certificate) >= 2 {
			return cert, nil
		}
		slog.Info("reissuing gRPC server certificate",
			"reason", "expiring, hostname change, missing CA in chain, or unreadable leaf")
	}

	certPEM, keyPEM, err := authority.IssueServerCert(hostname)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write %s: %w", keyPath, err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, fmt.Errorf("write %s: %w", certPath, err)
	}
	slog.Info("issued gRPC server certificate", "hostname", hostname)
	return tls.LoadX509KeyPair(certPath, keyPath)
}
