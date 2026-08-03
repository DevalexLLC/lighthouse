// Package httpapi is the dashboard: a session-authenticated JSON API under
// /api/v1 plus the embedded SPA. It shares the HTTPS listener run.go binds;
// agent traffic never comes through here (that is grpcapi, on the mTLS
// listener).
package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/devalexllc/lighthouse/internal/server/store"
)

// DB is the subset of *store.Store the dashboard needs. It is an interface
// so handler tests run offline against a fake instead of a live PostgreSQL.
type DB interface {
	GetUserByUsername(ctx context.Context, username string) (*store.UserInfo, error)
	CreateSession(ctx context.Context, userID uuid.UUID, tokenHash []byte, csrfToken string, expiresAt time.Time) error
	GetSessionByTokenHash(ctx context.Context, tokenHash []byte) (*store.SessionInfo, error)
	TouchSession(ctx context.Context, id uuid.UUID) error
	DeleteSessionByTokenHash(ctx context.Context, tokenHash []byte) error
	DeleteExpiredSessions(ctx context.Context) (int64, error)

	ListSites(ctx context.Context) ([]store.SiteInfo, error)
	ListAgents(ctx context.Context) ([]store.AgentListInfo, error)
	AgentHealthSeries(ctx context.Context, window, bucket time.Duration, excludeProbeType int16) ([]store.AgentHealthBucket, error)
	MatrixLatest(ctx context.Context, horizon time.Duration) ([]store.MatrixRow, error)
	ExpectedPairs(ctx context.Context) ([]store.SitePair, error)
	SiteEndpoints(ctx context.Context, siteName string) (*store.SiteEndpoints, error)
	PairSeries(ctx context.Context, srcAgents, dstTargets []uuid.UUID, bucket, window time.Duration, source store.Source, latencySource string) ([]store.SeriesBucket, error)
	PairSummary(ctx context.Context, srcAgents, dstTargets []uuid.UUID, window time.Duration, source store.Source) (*store.PairSummaryRow, error)
	PairLatencySource(ctx context.Context, srcAgents, dstTargets []uuid.UUID, window time.Duration, source store.Source) (string, error)
	DirectionLatest(ctx context.Context, srcAgents, dstTargets []uuid.UUID, horizon time.Duration) ([]store.MatrixRow, error)

	GetSettings(ctx context.Context) (*store.ThresholdSettings, error)
	UpdateSettings(ctx context.Context, ts store.ThresholdSettings) (*store.ThresholdSettings, error)

	ListTargets(ctx context.Context) ([]store.TargetInfo, error)
	UpsertExternalTarget(ctx context.Context, name, address string, port int32, url string) (uuid.UUID, error)
	DeleteTarget(ctx context.Context, name string) error
	ListMeshGroups(ctx context.Context) ([]store.MeshGroupInfo, error)
	UpsertMeshGroup(ctx context.Context, name string) (uuid.UUID, error)
	DeleteMeshGroup(ctx context.Context, name string) (int64, error)
	AddMeshMember(ctx context.Context, meshName, siteName string) error
	RemoveMeshMember(ctx context.Context, meshName, siteName string) error
	ListProbeConfigs(ctx context.Context) ([]store.ProbeConfigInfo, error)
	GetProbeConfig(ctx context.Context, id uuid.UUID) (*store.ProbeConfigInfo, error)
	AddDirectProbe(ctx context.Context, siteName, targetName string, ps store.ProbeSettings, updatedBy string) (uuid.UUID, error)
	AddMeshProbe(ctx context.Context, meshName string, ps store.ProbeSettings, updatedBy string) (uuid.UUID, error)
	UpdateProbeConfig(ctx context.Context, id uuid.UUID, ps store.ProbeSettings, enabled bool, updatedBy string) error
	DeleteProbeConfig(ctx context.Context, id uuid.UUID) error

	ListOutages(ctx context.Context, window time.Duration) ([]store.OutageInfo, error)
	ListPathEvents(ctx context.Context, window time.Duration) ([]store.PathEventInfo, error)
	CurrentPaths(ctx context.Context, srcAgents, dstTargets []uuid.UUID) ([]store.CurrentPath, error)
}

const (
	sessionCookie = "lighthouse_session"
	sessionTTL    = 7 * 24 * time.Hour
	// staleHorizon bounds "current" state: a series with no result inside
	// it renders as stale rather than trusting old data.
	staleHorizon = 10 * time.Minute
)

type api struct {
	db      db
	limiter *loginLimiter
}

// db wraps DB so internal helpers hang off a private type.
type db struct{ DB }

// New returns the dashboard handler: /healthz (open), /api/v1 (sessions),
// and the SPA from static for everything else.
func New(sdb DB, static fs.FS) http.Handler {
	a := &api{db: db{sdb}, limiter: newLoginLimiter(loginLimit, loginWindow)}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.Handle("POST /api/v1/auth/logout", a.withSession(a.handleLogout))
	mux.Handle("GET /api/v1/auth/me", a.withSession(a.handleMe))
	mux.Handle("GET /api/v1/sites", a.withSession(a.handleSites))
	mux.Handle("GET /api/v1/agents", a.withSession(a.handleAgents))
	mux.Handle("GET /api/v1/agents/health", a.withSession(a.handleAgentHealth))
	mux.Handle("GET /api/v1/matrix", a.withSession(a.handleMatrix))
	mux.Handle("GET /api/v1/settings", a.withSession(a.handleSettingsGet))
	// withSession outermost: it populates the session context requireRole
	// reads and enforces CSRF on the mutating method.
	mux.Handle("PUT /api/v1/settings",
		a.withSession(requireRole("admin", http.HandlerFunc(a.handleSettingsPut)).ServeHTTP))
	// Probe-workload config: reads any-session, writes admin-only (the
	// same withSession-outermost ordering as PUT /settings).
	adminWrite := func(h http.HandlerFunc) http.Handler {
		return a.withSession(requireRole("admin", h).ServeHTTP)
	}
	mux.Handle("GET /api/v1/config/probe-types", a.withSession(a.handleProbeTypes))
	mux.Handle("GET /api/v1/config/targets", a.withSession(a.handleTargetsGet))
	mux.Handle("POST /api/v1/config/targets", adminWrite(a.handleTargetPost))
	mux.Handle("DELETE /api/v1/config/targets/{name}", adminWrite(a.handleTargetDelete))
	mux.Handle("GET /api/v1/config/meshes", a.withSession(a.handleMeshesGet))
	mux.Handle("POST /api/v1/config/meshes", adminWrite(a.handleMeshPost))
	mux.Handle("DELETE /api/v1/config/meshes/{name}", adminWrite(a.handleMeshDelete))
	mux.Handle("POST /api/v1/config/meshes/{name}/members/{site}", adminWrite(a.handleMeshMemberPost))
	mux.Handle("DELETE /api/v1/config/meshes/{name}/members/{site}", adminWrite(a.handleMeshMemberDelete))
	mux.Handle("GET /api/v1/config/probes", a.withSession(a.handleProbesGet))
	mux.Handle("POST /api/v1/config/probes", adminWrite(a.handleProbePost))
	mux.Handle("PUT /api/v1/config/probes/{id}", adminWrite(a.handleProbePut))
	mux.Handle("DELETE /api/v1/config/probes/{id}", adminWrite(a.handleProbeDelete))
	mux.Handle("GET /api/v1/pairs/{a}/{b}", a.withSession(a.handlePair))
	mux.Handle("GET /api/v1/pairs/{a}/{b}/series", a.withSession(a.handleSeries))
	mux.Handle("GET /api/v1/outages", a.withSession(a.handleOutages))
	mux.Handle("GET /api/v1/path-events", a.withSession(a.handlePathEvents))
	mux.Handle("GET /api/v1/traceroute/{a}/{b}", a.withSession(a.handleTraceroute))

	// Unmatched API paths are JSON 404s; the SPA fallback must never
	// shadow the API namespace.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})

	mux.Handle("/", staticHandler(static))

	return withAPIHeaders(mux)
}

// withAPIHeaders sets response headers common to the API namespace.
func withAPIHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			h := w.Header()
			h.Set("Cache-Control", "no-store")
			h.Set("X-Content-Type-Options", "nosniff")
		}
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const sessionKey ctxKey = 0

// sessionFrom returns the authenticated session placed by withSession.
func sessionFrom(ctx context.Context) *store.SessionInfo {
	s, _ := ctx.Value(sessionKey).(*store.SessionInfo)
	return s
}

func withSessionCtx(ctx context.Context, s *store.SessionInfo) context.Context {
	return context.WithValue(ctx, sessionKey, s)
}
