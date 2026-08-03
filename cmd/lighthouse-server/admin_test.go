package main

import (
	"testing"
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

// Probe type parsing and cadence/train validation moved to
// internal/server/probeadmin (shared with the HTTP config API) and are
// tested there.

func TestValidateCoords(t *testing.T) {
	cases := []struct {
		name     string
		lat, lon float64
		wantErr  bool
	}{
		{"origin is a real coordinate", 0, 0, false},
		{"london", 51.5074, -0.1278, false},
		{"poles and date line", 90, 180, false},
		{"negative extremes", -90, -180, false},
		{"lat too high", 90.01, 0, true},
		{"lat too low", -90.01, 0, true},
		{"lon too high", 0, 180.01, true},
		{"lon too low", 0, -180.01, true},
	}
	for _, c := range cases {
		err := validateCoords(c.lat, c.lon)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: validateCoords(%g, %g) = %v, wantErr=%v", c.name, c.lat, c.lon, err, c.wantErr)
		}
	}
}
