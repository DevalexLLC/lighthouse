package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"

	"github.com/devalexllc/lighthouse/internal/server/auth"
	"github.com/devalexllc/lighthouse/internal/server/store"
)

// fakeDB implements DB in memory. Only the parts each test exercises are
// populated; unhandled paths return zero values.
type fakeDB struct {
	users    map[string]*store.UserInfo
	sessions map[string]*store.SessionInfo // key: string(token_hash)
}

func newFakeDB() *fakeDB {
	return &fakeDB{users: map[string]*store.UserInfo{}, sessions: map[string]*store.SessionInfo{}}
}

func (f *fakeDB) addUser(username, password, role string, disabled bool) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		panic(err)
	}
	f.users[username] = &store.UserInfo{
		ID: uuid.New(), Username: username, PasswordHash: hash, Role: role, Disabled: disabled,
	}
}

func (f *fakeDB) GetUserByUsername(_ context.Context, username string) (*store.UserInfo, error) {
	return f.users[username], nil
}

func (f *fakeDB) CreateSession(_ context.Context, userID uuid.UUID, tokenHash []byte, csrf string, expiresAt time.Time) error {
	var username, role string
	for _, u := range f.users {
		if u.ID == userID {
			username, role = u.Username, u.Role
		}
	}
	f.sessions[string(tokenHash)] = &store.SessionInfo{
		ID: uuid.New(), UserID: userID, Username: username, Role: role,
		CSRFToken: csrf, ExpiresAt: expiresAt, LastUsedAt: time.Now(),
	}
	return nil
}

func (f *fakeDB) GetSessionByTokenHash(_ context.Context, tokenHash []byte) (*store.SessionInfo, error) {
	s := f.sessions[string(tokenHash)]
	if s == nil || time.Now().After(s.ExpiresAt) {
		return nil, nil
	}
	return s, nil
}

func (f *fakeDB) TouchSession(_ context.Context, _ uuid.UUID) error { return nil }

func (f *fakeDB) DeleteSessionByTokenHash(_ context.Context, tokenHash []byte) error {
	delete(f.sessions, string(tokenHash))
	return nil
}

func (f *fakeDB) DeleteExpiredSessions(_ context.Context) (int64, error) { return 0, nil }

func (f *fakeDB) ListSites(_ context.Context) ([]store.SiteInfo, error) { return nil, nil }
func (f *fakeDB) ListAgents(_ context.Context) ([]store.AgentListInfo, error) {
	return nil, nil
}
func (f *fakeDB) MatrixLatest(_ context.Context, _ time.Duration) ([]store.MatrixRow, error) {
	return nil, nil
}
func (f *fakeDB) ExpectedPairs(_ context.Context) ([]store.SitePair, error) { return nil, nil }
func (f *fakeDB) SiteEndpoints(_ context.Context, _ string) (*store.SiteEndpoints, error) {
	return nil, nil
}
func (f *fakeDB) PairSeries(_ context.Context, _, _ []uuid.UUID, _, _ time.Duration) ([]store.SeriesBucket, error) {
	return nil, nil
}
func (f *fakeDB) PairSummary(_ context.Context, _, _ []uuid.UUID, _ time.Duration) (*store.PairSummaryRow, error) {
	return &store.PairSummaryRow{}, nil
}
func (f *fakeDB) DirectionLatest(_ context.Context, _, _ []uuid.UUID, _ time.Duration) ([]store.MatrixRow, error) {
	return nil, nil
}

var testDist = fstest.MapFS{
	"index.html":       {Data: []byte("<html>spa</html>")},
	"assets/app.js":    {Data: []byte("console.log('app')")},
	"assets/style.css": {Data: []byte("body{}")},
}

func newTestAPI(t *testing.T, f *fakeDB) http.Handler {
	t.Helper()
	return New(f, testDist)
}

func doLogin(t *testing.T, h http.Handler, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.7:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestLoginSuccess(t *testing.T) {
	f := newFakeDB()
	f.addUser("alice", "hunter22222", "admin", false)
	h := newTestAPI(t, f)

	w := doLogin(t, h, "alice", "hunter22222")
	if w.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200: %s", w.Code, w.Body)
	}
	var res struct {
		User struct{ Username, Role string }
		CSRFToken string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("bad login body: %v", err)
	}
	if res.User.Username != "alice" || res.User.Role != "admin" || res.CSRFToken == "" {
		t.Errorf("login body = %+v", res)
	}

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != sessionCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie flags wrong: %+v", c)
	}
	if len(f.sessions) != 1 {
		t.Errorf("sessions stored = %d, want 1", len(f.sessions))
	}
}

func TestLoginFailuresAreIdentical(t *testing.T) {
	f := newFakeDB()
	f.addUser("alice", "hunter22222", "viewer", false)
	f.addUser("mallory", "goodpassword", "viewer", true) // disabled
	h := newTestAPI(t, f)

	wrongPw := doLogin(t, h, "alice", "wrong-password")
	unknown := doLogin(t, h, "bob", "wrong-password")
	disabled := doLogin(t, h, "mallory", "goodpassword")

	for name, w := range map[string]*httptest.ResponseRecorder{
		"wrong password": wrongPw, "unknown user": unknown, "disabled user": disabled,
	} {
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: code = %d, want 401", name, w.Code)
		}
		if len(w.Result().Cookies()) != 0 {
			t.Errorf("%s: 401 must not set cookies", name)
		}
	}
	if wrongPw.Body.String() != unknown.Body.String() {
		t.Errorf("unknown-user and wrong-password bodies differ: %q vs %q",
			unknown.Body.String(), wrongPw.Body.String())
	}
}

func TestLoginRateLimit(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	var last *httptest.ResponseRecorder
	for range loginLimit + 1 {
		last = doLogin(t, h, "nobody", "irrelevant")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Errorf("attempt %d = %d, want 429", loginLimit+1, last.Code)
	}
}

// loginAndCookie logs in and returns the session cookie + CSRF token.
func loginAndCookie(t *testing.T, h http.Handler, f *fakeDB) (*http.Cookie, string) {
	t.Helper()
	f.addUser("alice", "hunter22222", "viewer", false)
	w := doLogin(t, h, "alice", "hunter22222")
	if w.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", w.Code, w.Body)
	}
	var res struct {
		CSRFToken string `json:"csrf_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	return w.Result().Cookies()[0], res.CSRFToken
}

func TestSessionRequired(t *testing.T) {
	h := newTestAPI(t, newFakeDB())
	for _, path := range []string{
		"/api/v1/auth/me", "/api/v1/sites", "/api/v1/agents", "/api/v1/matrix",
		"/api/v1/pairs/a/b", "/api/v1/pairs/a/b/series",
	} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without session = %d, want 401", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("GET %s: content-type %q, want JSON", path, ct)
		}
	}
}

func TestHealthzOpen(t *testing.T) {
	h := newTestAPI(t, newFakeDB())
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
		t.Errorf("healthz = %d %q", w.Code, w.Body.String())
	}
}

func TestMeAndLogoutCSRF(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	cookie, csrf := loginAndCookie(t, h, f)

	// me works with just the cookie.
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), csrf) {
		t.Fatalf("me = %d %s", w.Code, w.Body)
	}

	// logout without CSRF token → 403, session survives.
	req = httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("logout without csrf = %d, want 403", w.Code)
	}
	if len(f.sessions) != 1 {
		t.Errorf("session deleted despite missing CSRF token")
	}

	// wrong CSRF token → 403.
	req = httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", "wrong")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("logout with wrong csrf = %d, want 403", w.Code)
	}

	// correct CSRF token → session gone, cookie cleared.
	req = httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("logout = %d %s", w.Code, w.Body)
	}
	if len(f.sessions) != 0 {
		t.Errorf("session not deleted on logout")
	}
	cleared := w.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Errorf("logout must clear the cookie, got %+v", cleared)
	}

	// the old cookie no longer authenticates.
	req = httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("me after logout = %d, want 401", w.Code)
	}
}

func TestRequireRole(t *testing.T) {
	f := newFakeDB()
	h := New(f, testDist)
	cookie, _ := loginAndCookie(t, h, f) // alice is a viewer

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	a := &api{db: db{f}}
	guarded := a.withSession(func(w http.ResponseWriter, r *http.Request) {
		requireRole("admin", inner).ServeHTTP(w, r)
	})

	req := httptest.NewRequest("GET", "/anything", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	guarded.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer behind requireRole(admin) = %d, want 403", w.Code)
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	cookie, _ := loginAndCookie(t, h, f)

	req := httptest.NewRequest("GET", "/api/v1/nope", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown api path = %d, want 404", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "<html") {
		t.Errorf("API 404 served the SPA: %q", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("API 404 content-type = %q, want JSON", ct)
	}
}

func TestStaticSPAFallback(t *testing.T) {
	h := newTestAPI(t, newFakeDB())
	for _, path := range []string{"/", "/pair/nyc/lon", "/deep/link"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		body, _ := io.ReadAll(w.Result().Body)
		if w.Code != http.StatusOK || !strings.Contains(string(body), "spa") {
			t.Errorf("GET %s = %d %q, want index.html", path, w.Code, body)
		}
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("GET %s: missing CSP, got %q", path, csp)
		}
	}
	// real assets are served as themselves
	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "console.log") {
		t.Errorf("asset not served: %q", w.Body.String())
	}
}

func TestBadWindowAndMetric(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	cookie, _ := loginAndCookie(t, h, f)

	for _, url := range []string{
		"/api/v1/pairs/a/b?window=6d",
		"/api/v1/pairs/a/b/series?window=never",
		"/api/v1/pairs/a/b/series?metric=bogus",
	} {
		req := httptest.NewRequest("GET", url, nil)
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", url, w.Code)
		}
	}
}

func TestUnknownSiteIs404(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	cookie, _ := loginAndCookie(t, h, f)

	req := httptest.NewRequest("GET", "/api/v1/pairs/nowhere/lon?window=24h", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "nowhere") {
		t.Errorf("unknown site = %d %s, want 404 naming the site", w.Code, w.Body)
	}
}
