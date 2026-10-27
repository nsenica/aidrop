package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aidrop/internal/config"
	"aidrop/internal/storage"
)

func TestManagementCORSAllowsConfiguredProxyOrigin(t *testing.T) {
	repository, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	handler := NewHandler(config.Config{
		BearerToken:    "cors-test-secret",
		AllowedOrigins: []string{"https://aidrop.example"},
	}, repository)

	preflight := httptest.NewRequest(http.MethodOptions, "http://aidrop-internal/api/v1/projects/demo/artifacts/page", nil)
	preflight.Header.Set("Origin", "https://aidrop.example")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	preflight.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	preflightResponse := httptest.NewRecorder()
	handler.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d body=%s", preflightResponse.Code, preflightResponse.Body.String())
	}
	if got := preflightResponse.Header().Get("Access-Control-Allow-Origin"); got != "https://aidrop.example" {
		t.Errorf("allow origin=%q", got)
	}
	if got := preflightResponse.Header().Get("Access-Control-Allow-Methods"); got != managementCORS.methods {
		t.Errorf("allow methods=%q", got)
	}
	if got := preflightResponse.Header().Get("Access-Control-Allow-Headers"); got != managementCORS.headers {
		t.Errorf("allow headers=%q", got)
	}
	for _, want := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
		if !headerContainsToken(preflightResponse.Header().Values("Vary"), want) {
			t.Errorf("Vary=%q, want token %q", preflightResponse.Header().Values("Vary"), want)
		}
	}

	create := httptest.NewRequest(http.MethodPost, "http://aidrop-internal/api/v1/projects/demo/artifacts/page", strings.NewReader("<h1>proxy</h1>"))
	create.Header.Set("Origin", "https://aidrop.example")
	create.Header.Set("Authorization", "Bearer cors-test-secret")
	create.Header.Set("Content-Type", "text/html")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("authenticated REST create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	if got := createResponse.Header().Get("Access-Control-Allow-Origin"); got != "https://aidrop.example" {
		t.Errorf("actual request allow origin=%q", got)
	}
	if got := createResponse.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("REST Access-Control-Allow-Credentials=%q, want unset", got)
	}

	unauthorized := httptest.NewRequest(http.MethodGet, "http://aidrop-internal/api/v1/projects", nil)
	unauthorized.Header.Set("Origin", "https://aidrop.example")
	unauthorizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Errorf("REST request without bearer status=%d, want %d", unauthorizedResponse.Code, http.StatusUnauthorized)
	}
	if got := unauthorizedResponse.Header().Get("Access-Control-Allow-Origin"); got != "https://aidrop.example" {
		t.Errorf("unauthorized response allow origin=%q", got)
	}
}

func TestMCPCORSAllowsConfiguredProxyOriginPreflight(t *testing.T) {
	repository, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	handler := NewHandler(config.Config{
		BearerToken:    "cors-test-secret",
		AllowedOrigins: []string{"https://aidrop.example"},
	}, repository)
	request := httptest.NewRequest(http.MethodOptions, "http://aidrop-internal/mcp", nil)
	request.Header.Set("Origin", "https://aidrop.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "authorization, content-type, mcp-protocol-version")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("MCP preflight status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://aidrop.example" {
		t.Errorf("allow origin=%q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Methods"); got != mcpCORS.methods {
		t.Errorf("allow methods=%q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Headers"); got != mcpCORS.headers {
		t.Errorf("allow headers=%q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("MCP Access-Control-Allow-Credentials=%q, want unset", got)
	}
}

func TestManagementCORSRejectsUnconfiguredOriginsAndPreflights(t *testing.T) {
	handler, _ := newTestService(t, "cors-test-secret")
	tests := []struct {
		name        string
		origin      string
		method      string
		requestHead string
	}{
		{name: "unconfigured external origin", origin: "https://other.example", method: http.MethodGet},
		{name: "malformed origin with path", origin: "http://localhost/mcp", method: http.MethodGet},
		{name: "unsupported method", origin: "http://localhost", method: http.MethodPatch},
		{name: "unsupported header", origin: "http://localhost", method: http.MethodGet, requestHead: "x-private"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodOptions, "http://localhost/api/v1/projects", nil)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Access-Control-Request-Method", test.method)
			if test.requestHead != "" {
				request.Header.Set("Access-Control-Request-Headers", test.requestHead)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("preflight status=%d body=%s, want %d", response.Code, response.Body.String(), http.StatusForbidden)
			}
		})
	}

	duplicateOrigin := httptest.NewRequest(http.MethodOptions, "http://localhost/api/v1/projects", nil)
	duplicateOrigin.Header.Add("Origin", "http://localhost")
	duplicateOrigin.Header.Add("Origin", "http://localhost")
	duplicateOrigin.Header.Set("Access-Control-Request-Method", http.MethodGet)
	duplicateResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateResponse, duplicateOrigin)
	if duplicateResponse.Code != http.StatusForbidden {
		t.Errorf("duplicate Origin status=%d, want %d", duplicateResponse.Code, http.StatusForbidden)
	}
}

func headerContainsToken(values []string, want string) bool {
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), want) {
				return true
			}
		}
	}
	return false
}
