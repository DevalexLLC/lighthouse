// Package probeadmin is the single source of truth for probe-config
// validation: admin-facing type names, cadence/train rules, and the
// per-type param registry. Both the admin CLI and the HTTP config API
// validate through this package so the two surfaces cannot drift.
package probeadmin

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
)

// TypeNames maps admin-facing names to wire enum values.
var TypeNames = map[string]pb.ProbeType{
	"icmp":       pb.ProbeType_PROBE_TYPE_ICMP,
	"tcp":        pb.ProbeType_PROBE_TYPE_TCP,
	"tls":        pb.ProbeType_PROBE_TYPE_TLS,
	"http":       pb.ProbeType_PROBE_TYPE_HTTP,
	"dns":        pb.ProbeType_PROBE_TYPE_DNS,
	"traceroute": pb.ProbeType_PROBE_TYPE_TRACEROUTE,
}

// Names returns the accepted type names, sorted for deterministic output.
func Names() []string {
	names := make([]string, 0, len(TypeNames))
	for k := range TypeNames {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ParseType resolves an admin-facing type name.
func ParseType(name string) (pb.ProbeType, error) {
	t, ok := TypeNames[name]
	if !ok {
		return 0, fmt.Errorf("unknown probe type %q (accepted: %s)", name, strings.Join(Names(), ", "))
	}
	return t, nil
}

// TypeName is the reverse mapping for display.
func TypeName(t int16) string {
	for name, v := range TypeNames {
		if int16(v) == t {
			return name
		}
	}
	return fmt.Sprintf("type-%d", t)
}

// Prober defaults applied when train fields are zero; the validation below
// must budget with the same values the agent will actually use.
const (
	DefaultTrainCount   = 10
	DefaultTrainSpacing = 200 * time.Millisecond
)

// FieldNames carries the surface-specific spelling of each setting so one
// validator produces natural error text for both the CLI ("--interval")
// and the HTTP API ("interval_ms").
type FieldNames struct {
	Interval     string
	Timeout      string
	TrainCount   string
	TrainSpacing string
}

// ValidateSettings checks cadence and train rules, returning EVERY problem
// (settings.go convention: a request failing several ways gets the full
// list at once). The train must fit inside the per-run timeout because the
// agent budgets the whole train within it — a longer train would silently
// lose its tail.
func ValidateSettings(interval, timeout time.Duration, trainCount int, trainSpacing time.Duration, f FieldNames) []string {
	var problems []string
	if interval <= 0 || timeout <= 0 {
		problems = append(problems, fmt.Sprintf("%s and %s must be positive", f.Interval, f.Timeout))
	} else if timeout >= interval {
		problems = append(problems, fmt.Sprintf("%s (%s) must be shorter than %s (%s)", f.Timeout, timeout, f.Interval, interval))
	}
	switch {
	case trainCount < 0 || trainSpacing < 0:
		problems = append(problems, fmt.Sprintf("%s and %s must not be negative", f.TrainCount, f.TrainSpacing))
	case trainCount == 0:
		// Snapshot building only forwards spacing alongside a positive
		// count; accepting spacing alone would silently no-op on the agent.
		if trainSpacing > 0 {
			problems = append(problems, fmt.Sprintf("%s requires %s", f.TrainSpacing, f.TrainCount))
		}
	case timeout > 0:
		effSpacing := trainSpacing
		if effSpacing == 0 {
			effSpacing = DefaultTrainSpacing
		}
		if trainLen := time.Duration(trainCount) * effSpacing; trainLen >= timeout {
			problems = append(problems, fmt.Sprintf("train of %d × %s (%s) must fit inside %s (%s)",
				trainCount, effSpacing, trainLen, f.Timeout, timeout))
		}
	}
	return problems
}

// ParamKind tells a form (or validator) how to treat a param value.
type ParamKind string

const (
	KindString ParamKind = "string" // non-empty free text
	KindPort   ParamKind = "port"   // integer 1–65535
	KindBool   ParamKind = "bool"   // "true" or "false"
	KindEnum   ParamKind = "enum"   // one of Enum (case-insensitive)
	KindStatus ParamKind = "status" // exact HTTP status ("200") or class ("2xx")
)

// ParamSpec describes one type-specific param key. The registry is the only
// machine-readable statement of what the agent probers actually read —
// an unknown key silently no-ops on the agent, so writes reject anything
// not listed here (fail loud).
type ParamSpec struct {
	Key            string    `json:"key"`
	Hint           string    `json:"hint"`
	Kind           ParamKind `json:"kind"`
	Enum           []string  `json:"enum,omitempty"`
	RequiredMesh   bool      `json:"required_mesh,omitempty"`
	RequiredDirect bool      `json:"required_direct,omitempty"`
	MeshOnly       bool      `json:"mesh_only,omitempty"`
}

// meshPortSpec: mesh templates have no target row to carry a port, so tcp
// and tls templates take it as a param (read by meshexpand only) — direct
// probes must not set it because their target row already carries one.
var meshPortSpec = ParamSpec{
	Key: "port", Kind: KindPort, RequiredMesh: true, MeshOnly: true,
	Hint: "peer port for mesh templates",
}

// registry mirrors exactly what the probers read; keep in lockstep with
// internal/agent/probes/{tls,http,dns}.go and meshexpand's meshPort.
var registry = map[pb.ProbeType][]ParamSpec{
	pb.ProbeType_PROBE_TYPE_ICMP: {},
	pb.ProbeType_PROBE_TYPE_TCP:  {meshPortSpec},
	pb.ProbeType_PROBE_TYPE_TLS: {
		meshPortSpec,
		{Key: "tls.sni", Kind: KindString, Hint: "override the handshake server name (default: target host)"},
		{Key: "tls.insecure_skip_verify", Kind: KindBool, Hint: "skip certificate verification"},
	},
	pb.ProbeType_PROBE_TYPE_HTTP: {
		{Key: "http.method", Kind: KindString, Hint: "request method (default GET)"},
		{Key: "http.expect_status", Kind: KindStatus, Hint: `expected status: exact ("200") or class ("2xx"); default 200`},
		{Key: "http.insecure_skip_verify", Kind: KindBool, Hint: "skip certificate verification (self-signed endpoints)"},
	},
	pb.ProbeType_PROBE_TYPE_DNS: {
		{Key: "dns.qname", Kind: KindString, RequiredMesh: true, RequiredDirect: true, Hint: "name to query"},
		{Key: "dns.qtype", Kind: KindEnum, Hint: "query type (default A)",
			Enum: []string{"A", "AAAA", "CNAME", "MX", "NS", "PTR", "SOA", "SRV", "TXT"}},
		{Key: "dns.expect_rcode", Kind: KindEnum, Hint: "expected RCODE (default NOERROR)",
			Enum: []string{"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMPL", "REFUSED"}},
		{Key: "dns.resolver", Kind: KindString, Hint: "override resolver host:port (default: the target)"},
	},
	pb.ProbeType_PROBE_TYPE_TRACEROUTE: {},
}

// Params returns the registry entry for a type (nil for unknown types —
// callers reach this only after ParseType).
func Params(t pb.ProbeType) []ParamSpec {
	return registry[t]
}

// DirectOnly reports types that cannot be mesh templates. HTTP is the one
// case: the prober reads only Target.Url, and mesh expansion carries only
// the peer's address/port — an expanded HTTP mesh probe would fail on an
// empty URL every run.
func DirectOnly(t pb.ProbeType) bool {
	return t == pb.ProbeType_PROBE_TYPE_HTTP
}

// ValidateParams checks a param map against the registry for the given type
// and assignment mode, returning every problem.
func ValidateParams(t pb.ProbeType, mesh bool, params map[string]string) []string {
	var problems []string
	if mesh && DirectOnly(t) {
		problems = append(problems,
			fmt.Sprintf("%s probes cannot be mesh templates: mesh expansion carries only the peer address/port and the prober needs a URL", TypeName(int16(t))))
	}
	specs := registry[t]
	byKey := make(map[string]ParamSpec, len(specs))
	accepted := make([]string, 0, len(specs))
	for _, s := range specs {
		byKey[s.Key] = s
		accepted = append(accepted, s.Key)
	}

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		spec, ok := byKey[k]
		if !ok {
			acceptedText := "none"
			if len(accepted) > 0 {
				acceptedText = strings.Join(accepted, ", ")
			}
			problems = append(problems, fmt.Sprintf("params: unknown key %q for probe type %s (accepted: %s)",
				k, TypeName(int16(t)), acceptedText))
			continue
		}
		if spec.MeshOnly && !mesh {
			problems = append(problems, fmt.Sprintf("params: %q applies only to mesh probes (direct targets carry their own port)", k))
			continue
		}
		if p := validateValue(spec, params[k]); p != "" {
			problems = append(problems, p)
		}
	}

	for _, s := range specs {
		required := (mesh && s.RequiredMesh) || (!mesh && s.RequiredDirect)
		if required && params[s.Key] == "" {
			mode := ""
			if s.MeshOnly {
				mode = "mesh "
			}
			problems = append(problems, fmt.Sprintf("params: %q is required for %s%s probes", s.Key, mode, TypeName(int16(t))))
		}
	}
	return problems
}

func validateValue(spec ParamSpec, v string) string {
	switch spec.Kind {
	case KindPort:
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Sprintf("params: %q must be an integer between 1 and 65535, got %q", spec.Key, v)
		}
	case KindBool:
		if v != "true" && v != "false" {
			return fmt.Sprintf(`params: %q must be "true" or "false", got %q`, spec.Key, v)
		}
	case KindEnum:
		if !slices.Contains(spec.Enum, strings.ToUpper(v)) {
			return fmt.Sprintf("params: unsupported %s %q (accepted: %s)", spec.Key, v, strings.Join(spec.Enum, ", "))
		}
	case KindStatus:
		ok := len(v) == 3 && v[0] >= '1' && v[0] <= '5' &&
			((v[1] == 'x' && v[2] == 'x') || (v[1] >= '0' && v[1] <= '9' && v[2] >= '0' && v[2] <= '9'))
		if !ok {
			return fmt.Sprintf(`params: %q must be an exact status ("200") or a class ("2xx"), got %q`, spec.Key, v)
		}
	case KindString:
		if strings.TrimSpace(v) == "" {
			return fmt.Sprintf("params: %q must not be empty", spec.Key)
		}
		if spec.Key == "http.method" && strings.ContainsAny(v, " \t") {
			return fmt.Sprintf("params: %q must be a single HTTP method token, got %q", spec.Key, v)
		}
	}
	return ""
}
