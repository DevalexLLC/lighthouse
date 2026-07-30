package main

import (
	"strings"
	"testing"
	"time"

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
		"icmp":       pb.ProbeType_PROBE_TYPE_ICMP,
		"tcp":        pb.ProbeType_PROBE_TYPE_TCP,
		"tls":        pb.ProbeType_PROBE_TYPE_TLS,
		"http":       pb.ProbeType_PROBE_TYPE_HTTP,
		"dns":        pb.ProbeType_PROBE_TYPE_DNS,
		"traceroute": pb.ProbeType_PROBE_TYPE_TRACEROUTE,
	} {
		got, err := parseProbeType(name)
		if err != nil || got != want {
			t.Errorf("parseProbeType(%q) = %v, %v", name, got, err)
		}
	}
	// Fail loud: the error must name the accepted set.
	_, err := parseProbeType("smtp")
	if err == nil || !strings.Contains(err.Error(), "tcp") {
		t.Errorf("unknown type error must list accepted types, got: %v", err)
	}
}

func TestValidateTrain(t *testing.T) {
	cases := []struct {
		name    string
		count   int
		spacing time.Duration
		timeout time.Duration
		wantErr bool
	}{
		{"no train", 0, 0, 5 * time.Second, false},
		{"default train fits", 10, 0, 5 * time.Second, false}, // 10×200ms = 2s < 5s
		{"default train too long", 10, 0, 2 * time.Second, true},
		{"explicit spacing fits", 5, 100 * time.Millisecond, time.Second, false},
		{"explicit spacing too long", 20, 300 * time.Millisecond, 5 * time.Second, true},
		{"negative count", -1, 0, 5 * time.Second, true},
		{"negative spacing", 5, -time.Millisecond, 5 * time.Second, true},
	}
	for _, c := range cases {
		err := validateTrain(c.count, c.spacing, c.timeout)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: validateTrain(%d, %s, %s) = %v, wantErr=%v",
				c.name, c.count, c.spacing, c.timeout, err, c.wantErr)
		}
	}
}
