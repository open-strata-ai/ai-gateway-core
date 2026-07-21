package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// TestE2E_AgentLifecycle drives the full gateway over a real TCP listener
// (httptest.NewServer) — exercising CORS + the exact camelCase wire
// contract the portal's agentClient depends on (EU-05 authoring).
func TestE2E_AgentLifecycle(t *testing.T) {
	srv := httptest.NewServer(newServerWithSession().Routes())
	defer srv.Close()

	do := func(method, path, body string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("X-Tenant-Id", "local")
		req.Header.Set("Authorization", "Bearer local-dev-token")
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}

	// CORS preflight for the POST origin.
	pre, _ := do(http.MethodOptions, "/v1/agents", "",
		map[string]string{"Origin": "http://localhost:5174", "Access-Control-Request-Method": "POST"})
	if pre.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight: want 204, got %d", pre.StatusCode)
	}
	if pre.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5174" {
		t.Fatalf("preflight: missing allow-origin, headers=%v", pre.Header)
	}

	// 1) create
	cresp, cbody := do(http.MethodPost, "/v1/agents",
		`{"name":"E2E Agent","description":"t","modelBinding":{"model":"cloud-qwen-max","provider":"alibaba"}}`, nil)
	if cresp.StatusCode != http.StatusCreated {
		t.Fatalf("create: want 201, got %d body=%s", cresp.StatusCode, cbody)
	}
	// wire contract: camelCase keys the portal parses
	for _, key := range []string{`"id"`, `"name"`, `"modelBinding"`, `"status":"draft"`, `"createdAt"`} {
		if !strings.Contains(cbody, key) {
			t.Fatalf("create: response missing %s (got %s)", key, cbody)
		}
	}
	var created domain.AgentSpec
	_ = json.Unmarshal([]byte(cbody), &created)
	if created.ID == "" || created.ModelBinding == nil || created.ModelBinding.Model != "cloud-qwen-max" {
		t.Fatalf("create: unexpected spec %+v", created)
	}

	// 2) list envelope { agents: [...] } with the new id
	_, lbody := do(http.MethodGet, "/v1/agents", "", nil)
	if !strings.Contains(lbody, `"agents"`) || !strings.Contains(lbody, created.ID) {
		t.Fatalf("list: missing envelope/agent (got %s)", lbody)
	}

	// 3) get by id
	gresp, gbody := do(http.MethodGet, "/v1/agents/"+created.ID, "", nil)
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("get: want 200, got %d", gresp.StatusCode)
	}
	if !strings.Contains(gbody, `"name":"E2E Agent"`) {
		t.Fatalf("get: name not echoed (got %s)", gbody)
	}

	// 4) patch (rename + publish)
	_, pbody := do(http.MethodPatch, "/v1/agents/"+created.ID, `{"name":"Renamed","status":"published"}`, nil)
	if !strings.Contains(pbody, `"name":"Renamed"`) || !strings.Contains(pbody, `"status":"published"`) {
		t.Fatalf("patch: not applied (got %s)", pbody)
	}

	// 5) delete → 204
	dresp, _ := do(http.MethodDelete, "/v1/agents/"+created.ID, "", nil)
	if dresp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", dresp.StatusCode)
	}

	// 6) get after delete → 404
	g2, _ := do(http.MethodGet, "/v1/agents/"+created.ID, "", nil)
	if g2.StatusCode != http.StatusNotFound {
		t.Fatalf("get-after-delete: want 404, got %d", g2.StatusCode)
	}
}
