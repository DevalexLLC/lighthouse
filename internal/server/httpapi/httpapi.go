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
	MatrixLatest(ctx context.Context, horizon time.Duration) ([]store.MatrixRow, error)
	ExpectedPairs(ctx context.Context) ([]store.SitePair, error)
	SiteEndpoints(ctx context.Context, siteName string) (*store.SiteEndpoints, error)
	PairSeries(ctx context.Context, srcAgents, dstTargets []uuid.UUID, bucket, window time.Duration) ([]store.SeriesBucket, error)
	PairSummary(ctx context.Context, srcAgents, dstTargets []uuid.UUID, window time.Duration) (*store.PairSummaryRow, error)
	DirectionLatest(ctx context.Context, srcAgents, dstTargets []uuid.UUID, horizon time.Duration) ([]store.MatrixRow, error)
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
	mux.Handle("GET /api/v1/matrix", a.withSession(a.handleMatrix))
	mux.Handle("GET /api/v1/pairs/{a}/{b}", a.withSession(a.handlePair))
	mux.Handle("GET /api/v1/pairs/{a}/{b}/series", a.withSession(a.handleSeries))

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
