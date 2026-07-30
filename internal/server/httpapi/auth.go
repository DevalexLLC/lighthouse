package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/devalexllc/lighthouse/internal/server/auth"
)

const (
	loginLimit  = 10
	loginWindow = time.Minute
	// touchInterval rate-limits last_used_at writes: dashboard polling
	// would otherwise turn every page refresh into a session UPDATE.
	touchInterval = 5 * time.Minute
)

type userJSON struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type loginResponse struct {
	User      userJSON `json:"user"`
	CSRFToken string   `json:"csrf_token"`
}

// handleLogin authenticates a username/password and mints a session.
// Unknown-user and wrong-password responses are byte-identical, and unknown
// users still burn an argon2 verification so timing is uniform too.
func (a *api) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts; try again in a minute")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	user, err := a.db.GetUserByUsername(r.Context(), req.Username)
	if err != nil {
		internalError(w, "login lookup", err)
		return
	}
	hash := auth.DummyHash
	if user != nil && !user.Disabled {
		hash = user.PasswordHash
	}
	ok, err := auth.VerifyPassword(req.Password, hash)
	if err != nil {
		internalError(w, "verify password", err)
		return
	}
	if !ok || user == nil || user.Disabled {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Opportunistic cleanup keeps the sessions table bounded without a
	// background job; expired rows are invisible to lookups either way.
	if n, err := a.db.DeleteExpiredSessions(r.Context()); err != nil {
		slog.Warn("httpapi: delete expired sessions", "err", err)
	} else if n > 0 {
		slog.Debug("httpapi: cleaned expired sessions", "count", n)
	}

	token, tokenHash, err := auth.NewToken()
	if err != nil {
		internalError(w, "mint session token", err)
		return
	}
	csrf, _, err := auth.NewToken()
	if err != nil {
		internalError(w, "mint csrf token", err)
		return
	}
	expires := time.Now().Add(sessionTTL)
	if err := a.db.CreateSession(r.Context(), user.ID, tokenHash, csrf, expires); err != nil {
		internalError(w, "create session", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, loginResponse{
		User:      userJSON{Username: user.Username, Role: user.Role},
		CSRFToken: csrf,
	})
}

// handleLogout deletes the session and clears the cookie. Idempotent.
func (a *api) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := a.db.DeleteSessionByTokenHash(r.Context(), auth.HashToken(c.Value)); err != nil {
			internalError(w, "delete session", err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleMe restores the SPA's view of the session (user + CSRF token).
func (a *api) handleMe(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, loginResponse{
		User:      userJSON{Username: s.Username, Role: s.Role},
		CSRFToken: s.CSRFToken,
	})
}

// withSession authenticates the request's session cookie, enforces CSRF on
// non-GET methods, and stores the session in the request context.
func (a *api) withSession(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		s, err := a.db.GetSessionByTokenHash(r.Context(), auth.HashToken(c.Value))
		if err != nil {
			internalError(w, "session lookup", err)
			return
		}
		if s == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			got := r.Header.Get("X-CSRF-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.CSRFToken)) != 1 {
				writeError(w, http.StatusForbidden, "missing or invalid CSRF token")
				return
			}
		}
		if time.Since(s.LastUsedAt) > touchInterval {
			if err := a.db.TouchSession(r.Context(), s.ID); err != nil {
				slog.Warn("httpapi: touch session", "err", err)
			}
		}
		next.ServeHTTP(w, r.WithContext(withSessionCtx(r.Context(), s)))
	})
}

// requireRole guards a handler behind a role. M3 has no admin-only
// endpoints yet; M4's admin CRUD mounts behind requireRole("admin").
func requireRole(role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := sessionFrom(r.Context()); s == nil || s.Role != role {
			writeError(w, http.StatusForbidden, "requires "+role+" role")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP is the rate-limit key. The SNI proxy is a TCP passthrough, so
// RemoteAddr is the real client address (no X-Forwarded-For to trust or
// spoof).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginLimiter is a fixed-window per-IP counter. Windows reset lazily; the
// map is pruned on each reset so it cannot grow past one entry per active
// IP per window.
type loginLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	start    time.Time
	attempts map[string]int
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{limit: limit, window: window, start: time.Now(), attempts: map[string]int{}}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.start) > l.window {
		l.start = time.Now()
		clear(l.attempts)
	}
	l.attempts[ip]++
	return l.attempts[ip] <= l.limit
}
