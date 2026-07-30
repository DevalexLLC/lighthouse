package httpapi

import "time"

// windowSpec maps a query window to its raw-data bucket width. Buckets are
// sized for raw probe_results (M3); the M5 continuous aggregates will
// re-source the longer windows without changing this API. 24h is served
// beyond the documented 7d..365d list so a freshly seeded stack has a live
// chart to look at; it rides the identical code path.
type windowSpec struct {
	Window time.Duration
	Bucket time.Duration
}

var windows = map[string]windowSpec{
	"24h":  {24 * time.Hour, time.Minute},
	"7d":   {7 * 24 * time.Hour, 5 * time.Minute},
	"30d":  {30 * 24 * time.Hour, time.Hour},
	"90d":  {90 * 24 * time.Hour, 3 * time.Hour},
	"365d": {365 * 24 * time.Hour, 24 * time.Hour},
}

// parseWindow resolves a ?window= value; ok is false for anything not in
// the table (the handler answers 400, never a silent default).
func parseWindow(s string) (windowSpec, bool) {
	if s == "" {
		s = "24h"
	}
	spec, ok := windows[s]
	return spec, ok
}
