package probeadmin

import (
	"strings"
	"testing"
	"time"

	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
)

func TestParseType(t *testing.T) {
	for name, want := range TypeNames {
		got, err := ParseType(name)
		if err != nil || got != want {
			t.Errorf("ParseType(%q) = %v, %v; want %v", name, got, err, want)
		}
	}
	_, err := ParseType("smtp")
	if err == nil {
		t.Fatal("ParseType(smtp) succeeded")
	}
	// The accepted list must be sorted so error text is deterministic.
	want := `unknown probe type "smtp" (accepted: dns, http, icmp, tcp, tls, traceroute)`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestTypeName(t *testing.T) {
	if got := TypeName(int16(pb.ProbeType_PROBE_TYPE_DNS)); got != "dns" {
		t.Errorf("TypeName(dns) = %q", got)
	}
	if got := TypeName(99); got != "type-99" {
		t.Errorf("TypeName(99) = %q", got)
	}
}

var cliFields = FieldNames{
	Interval: "--interval", Timeout: "--timeout",
	TrainCount: "--train-count", TrainSpacing: "--train-spacing",
}

func TestValidateSettings(t *testing.T) {
	cases := []struct {
		name               string
		interval, timeout  time.Duration
		count              int
		spacing            time.Duration
		wantContains       []string
	}{
		{"ok", 30 * time.Second, 5 * time.Second, 0, 0, nil},
		{"ok train", 30 * time.Second, 5 * time.Second, 10, 200 * time.Millisecond, nil},
		{"non-positive", 0, 0, 0, 0, []string{"--interval and --timeout must be positive"}},
		{"timeout too long", 10 * time.Second, 10 * time.Second, 0, 0,
			[]string{"--timeout (10s) must be shorter than --interval (10s)"}},
		{"negative train", 30 * time.Second, 5 * time.Second, -1, 0,
			[]string{"--train-count and --train-spacing must not be negative"}},
		{"spacing without count", 30 * time.Second, 5 * time.Second, 0, time.Second,
			[]string{"--train-spacing requires --train-count"}},
		{"train too long", 30 * time.Second, 5 * time.Second, 30, 200 * time.Millisecond,
			[]string{"train of 30 × 200ms (6s) must fit inside --timeout (5s)"}},
		{"train default spacing", 30 * time.Second, time.Second, 10, 0,
			[]string{"train of 10 × 200ms (2s) must fit inside --timeout (1s)"}},
		{"multiple problems", 5 * time.Second, 10 * time.Second, -1, -1,
			[]string{"must be shorter than", "must not be negative"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ValidateSettings(c.interval, c.timeout, c.count, c.spacing, cliFields)
			if len(c.wantContains) == 0 {
				if len(got) != 0 {
					t.Fatalf("problems = %v, want none", got)
				}
				return
			}
			joined := strings.Join(got, "; ")
			for _, w := range c.wantContains {
				if !strings.Contains(joined, w) {
					t.Errorf("problems %q missing %q", joined, w)
				}
			}
			if len(got) != len(c.wantContains) {
				t.Errorf("got %d problems %v, want %d", len(got), got, len(c.wantContains))
			}
		})
	}
}

func TestValidateParams(t *testing.T) {
	cases := []struct {
		name         string
		typ          pb.ProbeType
		mesh         bool
		params       map[string]string
		wantContains []string
	}{
		{"icmp none ok", pb.ProbeType_PROBE_TYPE_ICMP, true, nil, nil},
		{"icmp unknown key", pb.ProbeType_PROBE_TYPE_ICMP, true, map[string]string{"port": "9"},
			[]string{`unknown key "port" for probe type icmp (accepted: none)`}},
		{"tcp mesh needs port", pb.ProbeType_PROBE_TYPE_TCP, true, nil,
			[]string{`"port" is required for mesh tcp probes`}},
		{"tcp mesh port ok", pb.ProbeType_PROBE_TYPE_TCP, true, map[string]string{"port": "5432"}, nil},
		{"tcp direct rejects port", pb.ProbeType_PROBE_TYPE_TCP, false, map[string]string{"port": "5432"},
			[]string{`"port" applies only to mesh probes`}},
		{"tcp direct ok", pb.ProbeType_PROBE_TYPE_TCP, false, nil, nil},
		{"port range", pb.ProbeType_PROBE_TYPE_TCP, true, map[string]string{"port": "70000"},
			[]string{`"port" must be an integer between 1 and 65535, got "70000"`}},
		{"port not int", pb.ProbeType_PROBE_TYPE_TCP, true, map[string]string{"port": "https"},
			[]string{`"port" must be an integer between 1 and 65535`}},
		{"tls bool bad", pb.ProbeType_PROBE_TYPE_TLS, true,
			map[string]string{"port": "443", "tls.insecure_skip_verify": "yes"},
			[]string{`"tls.insecure_skip_verify" must be "true" or "false", got "yes"`}},
		{"tls ok", pb.ProbeType_PROBE_TYPE_TLS, true,
			map[string]string{"port": "443", "tls.sni": "example.org", "tls.insecure_skip_verify": "true"}, nil},
		{"http status class ok", pb.ProbeType_PROBE_TYPE_HTTP, false,
			map[string]string{"http.expect_status": "4xx"}, nil},
		{"http status exact ok", pb.ProbeType_PROBE_TYPE_HTTP, false,
			map[string]string{"http.expect_status": "204"}, nil},
		{"http status bad", pb.ProbeType_PROBE_TYPE_HTTP, false,
			map[string]string{"http.expect_status": "6xx"},
			[]string{`"http.expect_status" must be an exact status ("200") or a class ("2xx"), got "6xx"`}},
		{"http method spaces", pb.ProbeType_PROBE_TYPE_HTTP, false,
			map[string]string{"http.method": "GET /"},
			[]string{`"http.method" must be a single HTTP method token`}},
		{"dns requires qname both modes", pb.ProbeType_PROBE_TYPE_DNS, false, nil,
			[]string{`"dns.qname" is required for dns probes`}},
		{"dns qtype case-insensitive", pb.ProbeType_PROBE_TYPE_DNS, true,
			map[string]string{"dns.qname": "example.org", "dns.qtype": "aaaa"}, nil},
		{"dns qtype bad", pb.ProbeType_PROBE_TYPE_DNS, true,
			map[string]string{"dns.qname": "example.org", "dns.qtype": "ANY"},
			[]string{`unsupported dns.qtype "ANY" (accepted: A, AAAA, CNAME, MX, NS, PTR, SOA, SRV, TXT)`}},
		{"dns rcode bad", pb.ProbeType_PROBE_TYPE_DNS, true,
			map[string]string{"dns.qname": "example.org", "dns.expect_rcode": "YES"},
			[]string{`unsupported dns.expect_rcode "YES"`}},
		{"empty string value", pb.ProbeType_PROBE_TYPE_DNS, true,
			map[string]string{"dns.qname": "  "},
			[]string{`"dns.qname" must not be empty`}},
		{"multiple problems", pb.ProbeType_PROBE_TYPE_TLS, true,
			map[string]string{"bogus": "1", "tls.insecure_skip_verify": "nah"},
			[]string{`unknown key "bogus"`, `must be "true" or "false"`, `"port" is required`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ValidateParams(c.typ, c.mesh, c.params)
			joined := strings.Join(got, "; ")
			if len(c.wantContains) == 0 {
				if len(got) != 0 {
					t.Fatalf("problems = %v, want none", got)
				}
				return
			}
			for _, w := range c.wantContains {
				if !strings.Contains(joined, w) {
					t.Errorf("problems %q missing %q", joined, w)
				}
			}
			if len(got) != len(c.wantContains) {
				t.Errorf("got %d problems %v, want %d", len(got), got, len(c.wantContains))
			}
		})
	}
}

// TestRegistryCoversAllTypes pins that every admin-facing type has a
// registry entry, so a future probe type cannot silently bypass param
// validation.
func TestRegistryCoversAllTypes(t *testing.T) {
	for name, typ := range TypeNames {
		if _, ok := registry[typ]; !ok {
			t.Errorf("probe type %s has no registry entry", name)
		}
	}
}
