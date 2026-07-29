package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `
listen:
  grpc_hostname: grpc.lighthouse.example.com
db:
  url: postgres://lh:lh@localhost:5432/lighthouse
tls:
  cert_file: /etc/lighthouse/server.crt
  key_file: /etc/lighthouse/server.key
`

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValidAppliesDefaults(t *testing.T) {
	cfg, err := Load(write(t, valid))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen.GRPC != ":8443" || cfg.Listen.HTTP != ":8080" {
		t.Fatalf("defaults not applied: %+v", cfg.Listen)
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("default log level not applied: %q", cfg.Log.Level)
	}
}

func TestLoadUnknownKeyNamed(t *testing.T) {
	_, err := Load(write(t, valid+"databse: oops\n"))
	if err == nil {
		t.Fatal("unknown key accepted")
	}
	if !strings.Contains(err.Error(), "databse") {
		t.Fatalf("error does not name the key: %v", err)
	}
}

func TestLoadMissingRequiredNamed(t *testing.T) {
	_, err := Load(write(t, "log:\n  level: info\n"))
	if err == nil {
		t.Fatal("missing required fields accepted")
	}
	for _, want := range []string{"db.url", "tls.cert_file", "tls.key_file", "listen.grpc_hostname"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestLoadBadLogLevel(t *testing.T) {
	_, err := Load(write(t, valid+"log:\n  level: verbose\n"))
	if err == nil || !strings.Contains(err.Error(), "verbose") {
		t.Fatalf("bad log level not rejected with value named: %v", err)
	}
}
