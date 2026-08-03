package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
	"github.com/devalexllc/lighthouse/internal/server/store"
)

// --- fakeDB implementation of the config methods ---

func (f *fakeDB) ListTargets(_ context.Context) ([]store.TargetInfo, error) { return f.targets, nil }

func (f *fakeDB) UpsertExternalTarget(_ context.Context, name, address string, port int32, url string) (uuid.UUID, error) {
	for i := range f.targets {
		if f.targets[i].Name == name {
			if f.targets[i].Kind != "external" {
				return uuid.Nil, fmt.Errorf("target %q already exists as an agent target%w", name, store.ErrConflict)
			}
			f.targets[i].Address, f.targets[i].Port, f.targets[i].URL = address, port, url
			return f.targets[i].ID, nil
		}
	}
	t := store.TargetInfo{ID: uuid.New(), Kind: "external", Name: name, Address: address, Port: port, URL: url}
	f.targets = append(f.targets, t)
	return t.ID, nil
}

func (f *fakeDB) DeleteTarget(_ context.Context, name string) error {
	for i, t := range f.targets {
		if t.Name == name && t.Kind == "external" {
			if t.ProbeCount > 0 {
				return store.InUseError{Name: name, Count: t.ProbeCount}
			}
			f.targets = append(f.targets[:i], f.targets[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("external target %q does not exist%w", name, store.ErrNotFound)
}

func (f *fakeDB) ListMeshGroups(_ context.Context) ([]store.MeshGroupInfo, error) {
	return f.meshes, nil
}

func (f *fakeDB) UpsertMeshGroup(_ context.Context, name string) (uuid.UUID, error) {
	for _, m := range f.meshes {
		if m.Name == name {
			return m.ID, nil
		}
	}
	m := store.MeshGroupInfo{ID: uuid.New(), Name: name}
	f.meshes = append(f.meshes, m)
	return m.ID, nil
}

func (f *fakeDB) DeleteMeshGroup(_ context.Context, name string) (int64, error) {
	for i, m := range f.meshes {
		if m.Name == name {
			f.meshes = append(f.meshes[:i], f.meshes[i+1:]...)
			return m.ProbeCount, nil
		}
	}
	return 0, fmt.Errorf("mesh group %q does not exist%w", name, store.ErrNotFound)
}

func (f *fakeDB) AddMeshMember(_ context.Context, meshName, siteName string) error {
	for i := range f.meshes {
		if f.meshes[i].Name == meshName {
			f.meshes[i].Sites = append(f.meshes[i].Sites, siteName)
			return nil
		}
	}
	return fmt.Errorf("mesh group %q does not exist%w", meshName, store.ErrNotFound)
}

func (f *fakeDB) RemoveMeshMember(_ context.Context, meshName, siteName string) error {
	for i := range f.meshes {
		if f.meshes[i].Name != meshName {
			continue
		}
		for j, s := range f.meshes[i].Sites {
			if s == siteName {
				f.meshes[i].Sites = append(f.meshes[i].Sites[:j], f.meshes[i].Sites[j+1:]...)
				return nil
			}
		}
		return fmt.Errorf("site %q is not a member of mesh %q%w", siteName, meshName, store.ErrNotFound)
	}
	return fmt.Errorf("mesh group %q does not exist%w", meshName, store.ErrNotFound)
}

func (f *fakeDB) ListProbeConfigs(_ context.Context) ([]store.ProbeConfigInfo, error) {
	return f.probes, nil
}

func (f *fakeDB) GetProbeConfig(_ context.Context, id uuid.UUID) (*store.ProbeConfigInfo, error) {
	for i := range f.probes {
		if f.probes[i].ID == id {
			p := f.probes[i]
			return &p, nil
		}
	}
	return nil, fmt.Errorf("probe config %s does not exist%w", id, store.ErrNotFound)
}

func (f *fakeDB) AddDirectProbe(_ context.Context, siteName, targetName string, ps store.ProbeSettings, updatedBy string) (uuid.UUID, error) {
	p := store.ProbeConfigInfo{
		ID: uuid.New(), Site: siteName, Target: targetName, ProbeType: ps.ProbeType,
		Interval: ps.Interval, Timeout: ps.Timeout, TrainCount: ps.TrainCount,
		TrainSpacing: ps.TrainSpacing, Params: ps.Params, Enabled: true, UpdatedBy: updatedBy,
	}
	f.probes = append(f.probes, p)
	return p.ID, nil
}

func (f *fakeDB) AddMeshProbe(_ context.Context, meshName string, ps store.ProbeSettings, updatedBy string) (uuid.UUID, error) {
	p := store.ProbeConfigInfo{
		ID: uuid.New(), Mesh: meshName, ProbeType: ps.ProbeType,
		Interval: ps.Interval, Timeout: ps.Timeout, TrainCount: ps.TrainCount,
		TrainSpacing: ps.TrainSpacing, Params: ps.Params, Enabled: true, UpdatedBy: updatedBy,
	}
	f.probes = append(f.probes, p)
	return p.ID, nil
}

func (f *fakeDB) UpdateProbeConfig(_ context.Context, id uuid.UUID, ps store.ProbeSettings, enabled bool, updatedBy string) error {
	for i := range f.probes {
		if f.probes[i].ID == id {
			f.probes[i].Interval, f.probes[i].Timeout = ps.Interval, ps.Timeout
			f.probes[i].TrainCount, f.probes[i].TrainSpacing = ps.TrainCount, ps.TrainSpacing
			f.probes[i].Params, f.probes[i].Enabled = ps.Params, enabled
			f.probes[i].UpdatedBy = updatedBy
			return nil
		}
	}
	return fmt.Errorf("probe config %s does not exist%w", id, store.ErrNotFound)
}

func (f *fakeDB) DeleteProbeConfig(_ context.Context, id uuid.UUID) error {
	for i := range f.probes {
		if f.probes[i].ID == id {
			f.probes = append(f.probes[:i], f.probes[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("probe config %s does not exist%w", id, store.ErrNotFound)
}

// --- helpers ---

// configLogin wraps settings_test.go's loginRole with a per-role username.
func configLogin(t *testing.T, h http.Handler, f *fakeDB, role string) (*http.Cookie, string) {
	t.Helper()
	return loginRole(t, h, f, "user-"+role, role)
}

func doConfig(t *testing.T, h http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body == "" {
		rd = bytes.NewReader(nil)
	} else {
		rd = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rd)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func errBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-JSON error body: %q", w.Body)
	}
	return body.Error
}

const validDirectProbe = `{"site":"nyc","target":"pg","type":"tcp","interval_ms":10000,"timeout_ms":5000,"train_count":0,"train_spacing_ms":0,"params":{}}`

// --- tests ---

func TestConfigAuth(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	viewerCookie, viewerCSRF := configLogin(t, h, f, "viewer")

	reads := []string{
		"/api/v1/config/probe-types", "/api/v1/config/targets",
		"/api/v1/config/meshes", "/api/v1/config/probes",
	}
	for _, path := range reads {
		if w := doConfig(t, h, "GET", path, "", nil, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("anon GET %s = %d, want 401", path, w.Code)
		}
		if w := doConfig(t, h, "GET", path, "", viewerCookie, ""); w.Code != http.StatusOK {
			t.Errorf("viewer GET %s = %d, want 200: %s", path, w.Code, w.Body)
		}
	}

	writes := []struct{ method, path, body string }{
		{"POST", "/api/v1/config/targets", `{"name":"x","address":"y"}`},
		{"DELETE", "/api/v1/config/targets/x", ""},
		{"POST", "/api/v1/config/meshes", `{"name":"m"}`},
		{"DELETE", "/api/v1/config/meshes/m", ""},
		{"POST", "/api/v1/config/meshes/m/members/nyc", ""},
		{"DELETE", "/api/v1/config/meshes/m/members/nyc", ""},
		{"POST", "/api/v1/config/probes", validDirectProbe},
		{"PUT", "/api/v1/config/probes/" + uuid.Nil.String(), validDirectProbe},
		{"DELETE", "/api/v1/config/probes/" + uuid.Nil.String(), ""},
	}
	for _, wr := range writes {
		if w := doConfig(t, h, wr.method, wr.path, wr.body, nil, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("anon %s %s = %d, want 401", wr.method, wr.path, w.Code)
		}
		if w := doConfig(t, h, wr.method, wr.path, wr.body, viewerCookie, viewerCSRF); w.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d, want 403", wr.method, wr.path, w.Code)
		}
	}

	// Admin without the CSRF header must be rejected before the handler.
	adminCookie, _ := configLogin(t, h, f, "admin")
	if w := doConfig(t, h, "POST", "/api/v1/config/meshes", `{"name":"m"}`, adminCookie, ""); w.Code != http.StatusForbidden {
		t.Errorf("admin write without CSRF = %d, want 403", w.Code)
	}
}

func TestConfigTargetValidationAndConflict(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	cookie, csrf := configLogin(t, h, f, "admin")

	// Every problem reported at once.
	w := doConfig(t, h, "POST", "/api/v1/config/targets", `{"name":"","port":70000}`, cookie, csrf)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid target = %d, want 400: %s", w.Code, w.Body)
	}
	msg := errBody(t, w)
	for _, want := range []string{"name is required", "address or url is required", "port must be between"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}

	// Unknown JSON field and trailing data are client bugs, not no-ops.
	for _, body := range []string{`{"name":"x","address":"y","bogus":1}`, `{"name":"x","address":"y"} {}`} {
		if w := doConfig(t, h, "POST", "/api/v1/config/targets", body, cookie, csrf); w.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, w.Code)
		}
	}

	// In-use target deletes are 409 with the count; unknown targets 404.
	f.targets = append(f.targets, store.TargetInfo{ID: uuid.New(), Kind: "external", Name: "pg", ProbeCount: 3})
	w = doConfig(t, h, "DELETE", "/api/v1/config/targets/pg", "", cookie, csrf)
	if w.Code != http.StatusConflict {
		t.Fatalf("in-use delete = %d, want 409: %s", w.Code, w.Body)
	}
	if msg := errBody(t, w); !strings.Contains(msg, "3 probe config(s)") {
		t.Errorf("409 message %q must carry the count", msg)
	}
	if w := doConfig(t, h, "DELETE", "/api/v1/config/targets/nope", "", cookie, csrf); w.Code != http.StatusNotFound {
		t.Errorf("unknown delete = %d, want 404", w.Code)
	}
}

func TestConfigProbeValidation(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	cookie, csrf := configLogin(t, h, f, "admin")

	cases := []struct {
		name, body   string
		wantContains string
	}{
		{"unknown type", `{"site":"a","target":"b","type":"smtp","interval_ms":10000,"timeout_ms":5000}`,
			`unknown probe type "smtp"`},
		{"mesh and site both", `{"site":"a","target":"b","mesh":"m","type":"tcp","interval_ms":10000,"timeout_ms":5000}`,
			"exactly one of mesh or site+target"},
		{"neither", `{"type":"tcp","interval_ms":10000,"timeout_ms":5000}`,
			"exactly one of mesh or site+target"},
		{"timeout >= interval", `{"site":"a","target":"b","type":"tcp","interval_ms":5000,"timeout_ms":5000}`,
			"timeout_ms (5s) must be shorter than interval_ms (5s)"},
		{"train too long", `{"site":"a","target":"b","type":"icmp","interval_ms":30000,"timeout_ms":2000,"train_count":20}`,
			"must fit inside timeout_ms"},
		{"unknown param key", `{"site":"a","target":"b","type":"icmp","interval_ms":10000,"timeout_ms":5000,"params":{"bogus":"1"}}`,
			`unknown key "bogus" for probe type icmp`},
		{"port on direct probe", `{"site":"a","target":"b","type":"tcp","interval_ms":10000,"timeout_ms":5000,"params":{"port":"9"}}`,
			`"port" applies only to mesh probes`},
		{"mesh tcp needs port", `{"mesh":"m","type":"tcp","interval_ms":10000,"timeout_ms":5000}`,
			`"port" is required for mesh tcp probes`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doConfig(t, h, "POST", "/api/v1/config/probes", c.body, cookie, csrf)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400: %s", w.Code, w.Body)
			}
			if msg := errBody(t, w); !strings.Contains(msg, c.wantContains) {
				t.Errorf("error %q missing %q", msg, c.wantContains)
			}
		})
	}

	// A valid create lands with the session username as updated_by.
	w := doConfig(t, h, "POST", "/api/v1/config/probes", validDirectProbe, cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("valid probe = %d: %s", w.Code, w.Body)
	}
	if len(f.probes) != 1 || f.probes[0].UpdatedBy != "user-admin" {
		t.Errorf("probes = %+v, want one row updated_by user-admin", f.probes)
	}
}

func TestConfigProbePutImmutableIdentity(t *testing.T) {
	f := newFakeDB()
	id := uuid.New()
	f.probes = []store.ProbeConfigInfo{{
		ID: id, Site: "nyc", Target: "pg", ProbeType: int16(pb.ProbeType_PROBE_TYPE_TCP),
		Interval: 10 * time.Second, Timeout: 5 * time.Second, Enabled: true,
	}}
	h := newTestAPI(t, f)
	cookie, csrf := configLogin(t, h, f, "admin")

	// Changing identity fields is rejected, each named.
	body := `{"site":"lon","target":"pg","type":"icmp","interval_ms":10000,"timeout_ms":5000}`
	w := doConfig(t, h, "PUT", "/api/v1/config/probes/"+id.String(), body, cookie, csrf)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("identity change = %d, want 400: %s", w.Code, w.Body)
	}
	msg := errBody(t, w)
	for _, want := range []string{"type cannot be changed", "site cannot be changed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}

	// A cadence edit (identity omitted) succeeds and keeps the ID.
	body = `{"interval_ms":20000,"timeout_ms":5000,"params":{}}`
	w = doConfig(t, h, "PUT", "/api/v1/config/probes/"+id.String(), body, cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("cadence edit = %d: %s", w.Code, w.Body)
	}
	var res struct {
		ID         string `json:"id"`
		IntervalMS int64  `json:"interval_ms"`
		Enabled    bool   `json:"enabled"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.ID != id.String() || res.IntervalMS != 20000 || !res.Enabled {
		t.Errorf("response = %+v", res)
	}

	// enabled:false round-trips (enable/disable is a full-object PUT).
	body = `{"interval_ms":20000,"timeout_ms":5000,"params":{},"enabled":false}`
	w = doConfig(t, h, "PUT", "/api/v1/config/probes/"+id.String(), body, cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("disable = %d: %s", w.Code, w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Enabled {
		t.Error("enabled should be false after disable PUT")
	}

	// Bad UUID and unknown ID.
	if w := doConfig(t, h, "PUT", "/api/v1/config/probes/not-a-uuid", body, cookie, csrf); w.Code != http.StatusBadRequest {
		t.Errorf("bad uuid = %d, want 400", w.Code)
	}
	if w := doConfig(t, h, "PUT", "/api/v1/config/probes/"+uuid.New().String(), body, cookie, csrf); w.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", w.Code)
	}
}

func TestConfigMeshDeleteReportsCascade(t *testing.T) {
	f := newFakeDB()
	f.meshes = []store.MeshGroupInfo{{ID: uuid.New(), Name: "core", Sites: []string{"nyc", "lon"}, ProbeCount: 2}}
	h := newTestAPI(t, f)
	cookie, csrf := configLogin(t, h, f, "admin")

	w := doConfig(t, h, "DELETE", "/api/v1/config/meshes/core", "", cookie, csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("mesh delete = %d: %s", w.Code, w.Body)
	}
	var res struct {
		ProbesDeleted int64 `json:"probes_deleted"`
	}
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.ProbesDeleted != 2 {
		t.Errorf("probes_deleted = %d, want 2", res.ProbesDeleted)
	}
}

func TestConfigProbeTypesRegistry(t *testing.T) {
	f := newFakeDB()
	h := newTestAPI(t, f)
	cookie, _ := configLogin(t, h, f, "viewer")

	w := doConfig(t, h, "GET", "/api/v1/config/probe-types", "", cookie, "")
	if w.Code != http.StatusOK {
		t.Fatalf("probe-types = %d: %s", w.Code, w.Body)
	}
	var res struct {
		Types []struct {
			Type   string `json:"type"`
			Params []struct {
				Key          string `json:"key"`
				Kind         string `json:"kind"`
				RequiredMesh bool   `json:"required_mesh"`
				MeshOnly     bool   `json:"mesh_only"`
			} `json:"params"`
		} `json:"types"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	byType := map[string][]string{}
	var tcpHasMeshOnlyPort bool
	for _, tt := range res.Types {
		for _, p := range tt.Params {
			byType[tt.Type] = append(byType[tt.Type], p.Key)
			if tt.Type == "tcp" && p.Key == "port" && p.MeshOnly && p.RequiredMesh {
				tcpHasMeshOnlyPort = true
			}
		}
	}
	if len(res.Types) != 6 {
		t.Errorf("types = %d, want 6", len(res.Types))
	}
	if !tcpHasMeshOnlyPort {
		t.Error("tcp must declare mesh-only required port")
	}
	if len(byType["icmp"]) != 0 {
		t.Errorf("icmp params = %v, want none", byType["icmp"])
	}
	if want := []string{"dns.qname", "dns.qtype", "dns.expect_rcode", "dns.resolver"}; !slicesEqual(byType["dns"], want) {
		t.Errorf("dns params = %v, want %v", byType["dns"], want)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
