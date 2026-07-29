// Package enroll implements agent enrollment and PKI state.
//
// Trust bootstrap is explicit — there is no trust-on-first-use: the operator
// supplies either the CA certificate file (--ca-cert) or its sha256
// fingerprint (--fingerprint, printed by `lighthouse-server token create`).
package enroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/devalexllc/lighthouse/internal/agent/config"
	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
	"github.com/devalexllc/lighthouse/internal/version"
)

const (
	keyFile      = "agent.key"
	certFile     = "agent.crt"
	caFile       = "ca.crt"
	agentIDFile  = "agent-id"
	csrStageFile = "enroll.csr"
)

// PKI is the agent's on-disk identity state under <state_dir>/pki.
type PKI struct {
	Dir string
}

func NewPKI(stateDir string) PKI {
	return PKI{Dir: filepath.Join(stateDir, "pki")}
}

// Enrolled reports whether a certificate is present.
func (p PKI) Enrolled() bool {
	_, err := os.Stat(filepath.Join(p.Dir, certFile))
	return err == nil
}

// Options controls a single enrollment.
type Options struct {
	Token string
	// Exactly one of CACertFile / Fingerprint provides the trust anchor.
	CACertFile string
	// Fingerprint is "sha256:<hex>" of the CA certificate.
	Fingerprint string
	// ProbeAddress peers should target (required behind NAT).
	ProbeAddress string
}

// Run enrolls against cfg.Server and writes the PKI state. Refuses to
// overwrite an existing enrollment.
func Run(ctx context.Context, cfg config.Config, opts Options) error {
	p := NewPKI(cfg.StateDir)
	if p.Enrolled() {
		return fmt.Errorf("already enrolled (%s exists); remove %s to re-enroll",
			filepath.Join(p.Dir, certFile), p.Dir)
	}
	if opts.Token == "" {
		return errors.New("--token is required")
	}
	tlsCfg, err := bootstrapTLS(cfg, opts)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("create pki dir: %w", err)
	}

	// Key and CSR are staged on disk BEFORE the RPC and reused on retry:
	// if the server commits the enrollment but the response is lost, the
	// retry presents the identical CSR and the server treats it as an
	// idempotent replay instead of a consumed token.
	key, csrDER, err := p.stageKeyAndCSR()
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()

	conn, err := grpc.NewClient(cfg.Server.Address,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return fmt.Errorf("connect %s: %w", cfg.Server.Address, err)
	}
	defer conn.Close()

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := pb.NewEnrollmentServiceClient(conn).Enroll(callCtx, &pb.EnrollRequest{
		JoinToken:    opts.Token,
		CsrDer:       csrDER,
		Hostname:     hostname,
		AgentVersion: version.Version,
		ProbeAddress: opts.ProbeAddress,
	})
	if err != nil {
		return fmt.Errorf("enrollment rejected by %s: %w", cfg.Server.Address, err)
	}

	if err := p.write(key, resp); err != nil {
		return err
	}
	os.Remove(filepath.Join(p.Dir, csrStageFile)) // staging no longer needed
	fmt.Printf("enrolled as agent %s (certificate valid until %s)\n",
		resp.GetAgentId(), resp.GetNotAfter().AsTime().Format(time.RFC3339))
	return nil
}

// parseStagedPair returns the staged private key iff the key parses, the
// CSR parses with a valid signature, and the CSR was made with that key.
func parseStagedPair(keyPEM, csrDER []byte) *ecdsa.PrivateKey {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil {
		return nil
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil
	}
	return key
}

// stageKeyAndCSR returns the staged key+CSR from an interrupted prior
// attempt, or generates and persists fresh ones.
func (p PKI) stageKeyAndCSR() (*ecdsa.PrivateKey, []byte, error) {
	keyPath := filepath.Join(p.Dir, keyFile)
	csrPath := filepath.Join(p.Dir, csrStageFile)

	keyPEM, keyErr := os.ReadFile(keyPath)
	csrDER, csrErr := os.ReadFile(csrPath)
	if keyErr == nil && csrErr == nil {
		if key := parseStagedPair(keyPEM, csrDER); key != nil {
			return key, csrDER, nil
		}
		// Corrupt or mismatched staging state (crash mid-write, stray
		// files): regenerate both rather than retrying garbage forever or
		// enrolling a certificate that does not match our key.
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	csrDER, err = x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create CSR: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	if err := os.WriteFile(keyPath,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, nil, fmt.Errorf("stage key: %w", err)
	}
	if err := os.WriteFile(csrPath, csrDER, 0o600); err != nil {
		return nil, nil, fmt.Errorf("stage CSR: %w", err)
	}
	return key, csrDER, nil
}

// write persists enrollment state. agent.crt is the "enrolled" marker
// (Enrolled checks it), so it is written LAST and atomically via rename: a
// failure part-way (full disk, crash) leaves no certificate file and the
// enrollment can simply be retried instead of wedging half-enrolled.
func (p PKI) write(key *ecdsa.PrivateKey, resp *pb.EnrollResponse) error {
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600},
		{caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.GetCaBundleDer()}), 0o644},
		{agentIDFile, []byte(resp.GetAgentId() + "\n"), 0o644},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(p.Dir, f.name), f.data, f.mode); err != nil {
			return fmt.Errorf("write %s: %w", f.name, err)
		}
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.GetCertDer()})
	tmp := filepath.Join(p.Dir, certFile+".tmp")
	if err := os.WriteFile(tmp, certPEM, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(p.Dir, certFile)); err != nil {
		return fmt.Errorf("commit %s: %w", certFile, err)
	}
	return nil
}

// ClientTLS builds the mTLS config for the agent uplink from enrolled state.
func (p PKI) ClientTLS(cfg config.Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(p.Dir, certFile), filepath.Join(p.Dir, keyFile))
	if err != nil {
		return nil, fmt.Errorf("agent certificate unusable (re-enroll?): %w", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(p.Dir, caFile))
	if err != nil {
		return nil, fmt.Errorf("CA bundle unusable (re-enroll?): %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("CA bundle contains no certificates")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName(cfg),
	}, nil
}

// bootstrapTLS builds the server-verification config used before the agent
// trusts anything.
func bootstrapTLS(cfg config.Config, opts Options) (*tls.Config, error) {
	name := serverName(cfg)
	switch {
	case opts.CACertFile != "" && opts.Fingerprint != "":
		// Silently preferring one would apply a different trust policy
		// than the caller asked for.
		return nil, errors.New("--ca-cert and --fingerprint are mutually exclusive; supply exactly one")

	case opts.CACertFile != "":
		caPEM, err := os.ReadFile(opts.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("--ca-cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("--ca-cert %s contains no certificates", opts.CACertFile)
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: name}, nil

	case opts.Fingerprint != "":
		want, err := parseFingerprint(opts.Fingerprint)
		if err != nil {
			return nil, err
		}
		// Custom verification: find the pinned CA in the presented chain,
		// then verify the leaf against it including hostname. Standard
		// verification is disabled (InsecureSkipVerify) because the pin IS
		// the trust anchor; this callback replaces it, never skips it.
		return &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         name,
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				return verifyByFingerprint(rawCerts, want, name)
			},
		}, nil

	default:
		return nil, errors.New("one of --ca-cert or --fingerprint is required (printed by `lighthouse-server token create`)")
	}
}

func verifyByFingerprint(rawCerts [][]byte, want [32]byte, hostname string) error {
	pool := x509.NewCertPool()
	var pinned bool
	certs := make([]*x509.Certificate, 0, len(rawCerts))
	for _, raw := range rawCerts {
		c, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("parse presented certificate: %w", err)
		}
		certs = append(certs, c)
		if sha256.Sum256(c.Raw) == want {
			pinned = true
			pool.AddCert(c)
		}
	}
	if !pinned {
		return errors.New("server did not present a certificate matching the pinned fingerprint")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: inter,
		DNSName:       hostname,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return fmt.Errorf("server certificate does not chain to the pinned CA: %w", err)
	}
	return nil
}

func parseFingerprint(s string) ([32]byte, error) {
	var out [32]byte
	s = strings.TrimPrefix(s, "sha256:")
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return out, fmt.Errorf("--fingerprint must be sha256:<64 hex chars>")
	}
	copy(out[:], raw)
	return out, nil
}

func serverName(cfg config.Config) string {
	if cfg.Server.SNI != "" {
		return cfg.Server.SNI
	}
	host, _, err := net.SplitHostPort(cfg.Server.Address)
	if err != nil {
		return cfg.Server.Address
	}
	return host
}
