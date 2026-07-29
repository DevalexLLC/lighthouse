package main

import (
	"strings"
	"testing"

	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
)

func TestParamsFlag(t *testing.T) {
	p := paramsFlag{}
	if err := p.Set("http.expect_status=200"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := p.Set("port=5432"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if p["http.expect_status"] != "200" || p["port"] != "5432" {
		t.Errorf("params = %v", p)
	}
	// Values may themselves contain '=': only the first split counts.
	if err := p.Set("k=a=b"); err != nil || p["k"] != "a=b" {
		t.Errorf("Set(k=a=b): err=%v val=%q", err, p["k"])
	}
	for _, bad := range []string{"noequals", "=value", ""} {
		if err := p.Set(bad); err == nil {
			t.Errorf("Set(%q): expected error", bad)
		}
	}
}

func TestParseProbeType(t *testing.T) {
	for name, want := range map[string]pb.ProbeType{
		"tcp":  pb.ProbeType_PROBE_TYPE_TCP,
		"tls":  pb.ProbeType_PROBE_TYPE_TLS,
		"http": pb.ProbeType_PROBE_TYPE_HTTP,
	} {
		got, err := parseProbeType(name)
		if err != nil || got != want {
			t.Errorf("parseProbeType(%q) = %v, %v", name, got, err)
		}
	}
	// Fail loud: the error must name the accepted set.
	_, err := parseProbeType("icmp")
	if err == nil || !strings.Contains(err.Error(), "tcp") {
		t.Errorf("unknown type error must list accepted types, got: %v", err)
	}
}
