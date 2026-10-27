package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"aidrop/internal/config"
	"aidrop/internal/storage"
)

func TestHealthIsPublic(t *testing.T) {
	handler := newTestHandler(t, "secret-value")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("content type = %q, want JSON", got)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("health status = %q, want ok", body["status"])
	}
}

func TestMermaidRendererAssetIsServedFromSameOrigin(t *testing.T) {
	handler := newTestHandler(t, "secret-value")
	response := performRequest(handler, http.MethodGet, "/assets/mermaid.tiny.min.js", "", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("Mermaid asset status=%d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Mermaid asset content type=%q, want JavaScript", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("Mermaid asset X-Content-Type-Options=%q, want nosniff", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("Mermaid asset Cache-Control=%q, want one-day public cache", got)
	}
	if !strings.Contains(response.Body.String(), "globalThis.mermaid") {
		t.Error("served Mermaid asset does not expose the expected browser renderer")
	}
}

func TestPlantUMLRendererFrameIsSandboxedAndCDNBound(t *testing.T) {
	handler := newTestHandler(t, "secret-value")
	response := performRequest(handler, http.MethodGet, "/assets/plantuml-frame.html", "", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("PlantUML frame status=%d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("PlantUML frame content type=%q, want HTML", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("PlantUML frame Cache-Control=%q, want no-store", got)
	}
	if got := response.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("PlantUML frame X-Frame-Options=%q, want SAMEORIGIN", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("PlantUML frame X-Content-Type-Options=%q, want nosniff", got)
	}
	csp := response.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'none'",
		"script-src 'unsafe-inline' 'wasm-unsafe-eval' https://unpkg.com/@plantuml/core@1.2026.9/",
		"worker-src blob:",
		"connect-src 'none'",
		"sandbox allow-scripts",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("PlantUML frame CSP=%q, missing %q", csp, want)
		}
	}
	for _, want := range []string{
		"window.PLANTUML_STDLIB_BASE = \"https://unpkg.com/@plantuml/core@1.2026.9/\"",
		"https://unpkg.com/@plantuml/core@1.2026.9/viz-global.js",
		"https://unpkg.com/@plantuml/core@1.2026.9/plantuml.js",
		`<div id="plantuml-output"></div>`,
		`#plantuml-output svg { display: block; width: 100%; height: auto; margin: 0 auto; }`,
		`document.getElementById("plantuml-output")`,
		`renderToString(source.split(/\r\n|\r|\n/), svgSource =>`,
		`/PlantUML version\b[\s\S]*Syntax Error\?/i`,
		`/(?:-->>|-->|->>|->)\s*:/m`,
		`{maxSvgSize:8192}`,
		"postMessage",
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("PlantUML frame does not contain %q", want)
		}
	}
	if strings.Contains(response.Body.String(), "allow-same-origin") {
		t.Error("PlantUML frame enables allow-same-origin")
	}
	if strings.Contains(response.Body.String(), "includeurl") {
		t.Error("PlantUML frame exposes remote include support")
	}
}

func TestManagementRoutesRequireBearerToken(t *testing.T) {
	const secret = "configured-secret"
	handler := newTestHandler(t, secret)
	tests := []struct {
		name   string
		header string
		cookie string
		query  string
		want   int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "invalid", header: "Bearer wrong-secret", want: http.StatusUnauthorized},
		{name: "cookie is not accepted", cookie: secret, want: http.StatusUnauthorized},
		{name: "query is not accepted", query: "?token=" + secret, want: http.StatusUnauthorized},
		{name: "valid", header: "Bearer " + secret, want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/projects"+tt.query, nil)
			if tt.header != "" {
				request.Header.Set("Authorization", tt.header)
			}
			if tt.cookie != "" {
				request.AddCookie(&http.Cookie{Name: "token", Value: tt.cookie})
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tt.want, recorder.Body.String())
			}
			if tt.want == http.StatusUnauthorized {
				if strings.Contains(recorder.Body.String(), secret) {
					t.Errorf("unauthorized response leaked configured token")
				}
				if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
					t.Errorf("content type = %q, want JSON", got)
				}
			}
		})
	}
}

func TestManagementRootIsProtected(t *testing.T) {
	handler := newTestHandler(t, "secret-value")
	for _, path := range []string{"/api/v1", "/api/v1/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want %d", path, recorder.Code, http.StatusUnauthorized)
		}
	}
}

func newTestHandler(t *testing.T, token string) http.Handler {
	handler, _ := newTestService(t, token)
	return handler
}

func newTestService(t *testing.T, token string) (http.Handler, *storage.Repository) {
	t.Helper()
	repository, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open test repository: %v", err)
	}
	return NewHandler(config.Config{BearerToken: token}, repository), repository
}

func assertPublicArtifactHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag=%q, want noindex, nofollow", got)
	}
	if got := response.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy=%q, want no-referrer", got)
	}
}

func TestHTMLPublishViewUpdateSourceMetadataDelete(t *testing.T) {
	const token = "integration-secret"
	handler, _ := newTestService(t, token)
	path := "/api/v1/projects/demo/artifacts/homepage"
	publicPath := "/p/demo/homepage/"
	original := []byte(`<!doctype html><html><head><link rel="stylesheet" href="styles/site.css"></head><body><h1>first</h1></body></html>`)

	created := performRequest(handler, http.MethodPost, path, token, "text/html; charset=utf-8", original)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body=%s; want %d", created.Code, created.Body.String(), http.StatusCreated)
	}
	var createdMetadata storage.Metadata
	if err := json.Unmarshal(created.Body.Bytes(), &createdMetadata); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createdMetadata.PublicURL != publicPath {
		t.Fatalf("public URL = %q, want %q", createdMetadata.PublicURL, publicPath)
	}

	viewed := performRequest(handler, http.MethodGet, publicPath, "", "", nil)
	if viewed.Code != http.StatusOK {
		t.Fatalf("public viewer status=%d body=%q; want %d", viewed.Code, viewed.Body.Bytes(), http.StatusOK)
	}
	assertPublicArtifactHeaders(t, viewed)
	head := performRequest(handler, http.MethodHead, publicPath, "", "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD public HTML preview status=%d body length=%d, want 200 with no body", head.Code, head.Body.Len())
	}
	assertPublicArtifactHeaders(t, head)
	if got := viewed.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("public Content-Type = %q, want text/html", got)
	}
	if got := viewed.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-src 'self'") || !strings.Contains(got, "default-src 'none'") {
		t.Errorf("viewer CSP = %q, want a restrictive policy allowing the same-origin frame", got)
	}
	if got := viewed.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("viewer X-Content-Type-Options = %q, want nosniff", got)
	}
	if strings.Contains(viewed.Body.String(), token) {
		t.Error("viewer page leaked the management bearer token")
	}
	for _, want := range []string{`href="/">Home</a>`, `href="/p/demo/">demo</a>`, `src="/p/demo/homepage/?_aidrop_content=1"`, `sandbox="allow-scripts"`} {
		if !strings.Contains(viewed.Body.String(), want) {
			t.Errorf("public viewer does not contain %q: %s", want, viewed.Body.String())
		}
	}
	if !strings.HasSuffix(createdMetadata.PublicURL, "/") {
		t.Errorf("directory-style public URL was not preserved: %q", createdMetadata.PublicURL)
	}
	rawViewed := performRequest(handler, http.MethodGet, publicPath+"?_aidrop_content=1", "", "", nil)
	if rawViewed.Code != http.StatusOK || !bytes.Equal(rawViewed.Body.Bytes(), original) {
		t.Fatalf("framed HTML view status=%d body=%q; want original HTML", rawViewed.Code, rawViewed.Body.Bytes())
	}
	if got := rawViewed.Header().Get("Content-Security-Policy"); got != "sandbox allow-scripts" || strings.Contains(got, "allow-same-origin") {
		t.Errorf("framed HTML CSP = %q, want sandbox allow-scripts without same-origin", got)
	}
	if !bytes.Contains(rawViewed.Body.Bytes(), []byte(`href="styles/site.css"`)) {
		t.Errorf("relative stylesheet reference was not preserved in framed HTML")
	}
	missingRelativeAsset := performRequest(handler, http.MethodGet, publicPath+"styles/site.css", "", "", nil)
	if missingRelativeAsset.Code != http.StatusNotFound || bytes.Equal(missingRelativeAsset.Body.Bytes(), original) {
		t.Errorf("unuploaded relative asset path status=%d body unexpectedly returned the HTML artifact", missingRelativeAsset.Code)
	}

	duplicate := performRequest(handler, http.MethodPost, path, token, "text/html", original)
	assertAPIError(t, duplicate, http.StatusConflict, "conflict")
	missingPut := performRequest(handler, http.MethodPut, "/api/v1/projects/demo/artifacts/missing", token, "text/html", []byte("<p>missing</p>"))
	assertAPIError(t, missingPut, http.StatusNotFound, "not_found")

	metadataResponse := performRequest(handler, http.MethodGet, path, token, "", nil)
	if metadataResponse.Code != http.StatusOK {
		t.Fatalf("metadata GET status=%d body=%s", metadataResponse.Code, metadataResponse.Body.String())
	}
	var fetchedMetadata storage.Metadata
	if err := json.Unmarshal(metadataResponse.Body.Bytes(), &fetchedMetadata); err != nil {
		t.Fatalf("decode metadata response: %v", err)
	}
	if fetchedMetadata != createdMetadata {
		t.Errorf("metadata GET = %+v, want %+v", fetchedMetadata, createdMetadata)
	}

	updatedSource := []byte(`<!doctype html><html><body><h1>updated</h1></body></html>`)
	updated := performRequest(handler, http.MethodPut, path, token, "text/html", updatedSource)
	if updated.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", updated.Code, updated.Body.String())
	}
	var updatedMetadata storage.Metadata
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedMetadata); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updatedMetadata.PublicURL != createdMetadata.PublicURL || !updatedMetadata.CreatedAt.Equal(createdMetadata.CreatedAt) {
		t.Errorf("update changed stable identity: created=%+v updated=%+v", createdMetadata, updatedMetadata)
	}
	viewed = performRequest(handler, http.MethodGet, publicPath, "", "", nil)
	if viewed.Code != http.StatusOK || !strings.Contains(viewed.Body.String(), `src="/p/demo/homepage/?_aidrop_content=1"`) {
		t.Fatalf("updated public viewer status=%d body=%q", viewed.Code, viewed.Body.Bytes())
	}
	rawViewed = performRequest(handler, http.MethodGet, publicPath+"?_aidrop_content=1", "", "", nil)
	if rawViewed.Code != http.StatusOK || !bytes.Equal(rawViewed.Body.Bytes(), updatedSource) {
		t.Fatalf("updated framed HTML status=%d body=%q", rawViewed.Code, rawViewed.Body.Bytes())
	}

	source := performRequest(handler, http.MethodGet, path+"/source", token, "", nil)
	if source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), updatedSource) {
		t.Fatalf("source response status=%d body=%q", source.Code, source.Body.Bytes())
	}
	if got := source.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("source Content-Type = %q, want application/octet-stream", got)
	}

	deleted := performRequest(handler, http.MethodDelete, path, token, "", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if got := performRequest(handler, http.MethodGet, publicPath, "", "", nil); got.Code != http.StatusNotFound {
		t.Errorf("public view after delete status=%d, want 404", got.Code)
	}
}

func TestHTMLUploadValidationDoesNotPublishContent(t *testing.T) {
	const token = "validation-secret"
	handler, repository := newTestService(t, token)
	tests := []struct {
		name        string
		path        string
		headerToken string
		contentType string
		body        []byte
		status      int
		code        string
	}{
		{
			name:        "unsupported content type",
			path:        "/api/v1/projects/demo/artifacts/type",
			headerToken: token,
			contentType: "text/plain",
			body:        []byte("plain text"),
			status:      http.StatusUnsupportedMediaType,
			code:        "unsupported_media_type",
		},
		{
			name:        "empty body",
			path:        "/api/v1/projects/demo/artifacts/empty",
			headerToken: token,
			contentType: "text/html",
			status:      http.StatusBadRequest,
			code:        "empty_payload",
		},
		{
			name:        "oversized body",
			path:        "/api/v1/projects/demo/artifacts/large",
			headerToken: token,
			contentType: "text/html",
			body:        bytes.Repeat([]byte("x"), maxHTMLBody+1),
			status:      http.StatusRequestEntityTooLarge,
			code:        "payload_too_large",
		},
		{
			name:        "invalid slug",
			path:        "/api/v1/projects/BadProject/artifacts/invalid",
			headerToken: token,
			contentType: "text/html",
			body:        []byte("<p>body</p>"),
			status:      http.StatusBadRequest,
			code:        "invalid_slug",
		},
		{
			name:        "unauthorized",
			path:        "/api/v1/projects/demo/artifacts/unauthorized",
			contentType: "text/html",
			body:        []byte("<p>secret</p>"),
			status:      http.StatusUnauthorized,
			code:        "unauthorized",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := performRequest(handler, http.MethodPost, tt.path, tt.headerToken, tt.contentType, tt.body)
			assertAPIError(t, response, tt.status, tt.code)
			if response.Body.Len() > 512 {
				t.Errorf("error response is not bounded: %d bytes", response.Body.Len())
			}
		})
	}
	projects, err := repository.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("validation failures published visible projects: %v", projects)
	}
}

func TestZIPPublishServesAssetsAndPreservesOriginalSource(t *testing.T) {
	const token = "zip-integration-secret"
	handler, _ := newTestService(t, token)
	path := "/api/v1/projects/demo/artifacts/site"
	publicPath := "/p/demo/site/"
	indexHTML := []byte(`<link rel="stylesheet" href="css/site.css"><script src="js/app.js"></script><img src="images/logo.svg"><a href="nested/">nested</a>`)
	archive := makeTestZIP(t, map[string][]byte{
		"index.html":          indexHTML,
		"css/site.css":        []byte("body { color: blue }"),
		"js/app.js":           []byte("console.log('site')"),
		"images/logo.svg":     []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"),
		"fonts/site.woff2":    []byte{0, 1, 2, 3},
		"nested/index.html":   []byte("<h1>Nested page</h1>"),
		"nested/page/page.js": []byte("console.log('nested')"),
	})

	created := performRequest(handler, http.MethodPost, path, token, "application/zip", archive)
	if created.Code != http.StatusCreated {
		t.Fatalf("ZIP POST status=%d body=%s; want %d", created.Code, created.Body.String(), http.StatusCreated)
	}
	var metadata storage.Metadata
	if err := json.Unmarshal(created.Body.Bytes(), &metadata); err != nil {
		t.Fatalf("decode ZIP create response: %v", err)
	}
	if metadata.Format != storage.FormatZIP || metadata.EntryPoint != "index.html" || metadata.PublicURL != publicPath {
		t.Fatalf("ZIP metadata = %+v, want ZIP index entry at %s", metadata, publicPath)
	}

	root := performRequest(handler, http.MethodGet, publicPath, "", "", nil)
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), `src="/p/demo/site/?_aidrop_content=1"`) || !strings.Contains(root.Body.String(), `href="/p/demo/">demo</a>`) {
		t.Fatalf("public viewer status=%d body=%q", root.Code, root.Body.Bytes())
	}
	if got := root.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-src 'self'") || !strings.Contains(got, "default-src 'none'") {
		t.Errorf("viewer CSP = %q, want a restrictive policy allowing the same-origin frame", got)
	}
	framedRoot := performRequest(handler, http.MethodGet, publicPath+"?_aidrop_content=1", "", "", nil)
	if framedRoot.Code != http.StatusOK || !bytes.Equal(framedRoot.Body.Bytes(), indexHTML) {
		t.Fatalf("framed ZIP root status=%d body=%q; want original entry point", framedRoot.Code, framedRoot.Body.Bytes())
	}
	if got := framedRoot.Header().Get("Content-Security-Policy"); got != "sandbox allow-scripts" || strings.Contains(got, "allow-same-origin") {
		t.Errorf("framed ZIP root CSP = %q, want sandbox allow-scripts without same-origin", got)
	}
	assertPublicArtifactHeaders(t, framedRoot)
	headRoot := performRequest(handler, http.MethodHead, publicPath, "", "", nil)
	if headRoot.Code != http.StatusOK || headRoot.Body.Len() != 0 {
		t.Errorf("HEAD public ZIP preview status=%d body length=%d, want 200 with no body", headRoot.Code, headRoot.Body.Len())
	}
	assertPublicArtifactHeaders(t, headRoot)
	headNested := performRequest(handler, http.MethodHead, publicPath+"nested/", "", "", nil)
	if headNested.Code != http.StatusOK || headNested.Body.Len() != 0 {
		t.Errorf("HEAD nested ZIP page status=%d body length=%d, want 200 with no body", headNested.Code, headNested.Body.Len())
	}
	assertPublicArtifactHeaders(t, headNested)
	assetCases := []struct {
		path        string
		contentType string
		body        []byte
	}{
		{path: "css/site.css", contentType: "text/css", body: []byte("body { color: blue }")},
		{path: "js/app.js", contentType: "text/javascript", body: []byte("console.log('site')")},
		{path: "images/logo.svg", contentType: "image/svg+xml", body: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")},
		{path: "fonts/site.woff2", contentType: "font/woff2", body: []byte{0, 1, 2, 3}},
		{path: "nested/page/page.js", contentType: "text/javascript", body: []byte("console.log('nested')")},
	}
	for _, asset := range assetCases {
		response := performRequest(handler, http.MethodGet, publicPath+asset.path, "", "", nil)
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), asset.body) {
			t.Errorf("GET %s status=%d body=%q; want %q", asset.path, response.Code, response.Body.Bytes(), asset.body)
		}
		if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, asset.contentType) {
			t.Errorf("GET %s Content-Type=%q, want prefix %q", asset.path, got, asset.contentType)
		}
		if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s X-Content-Type-Options=%q, want nosniff", asset.path, got)
		}
		assertPublicArtifactHeaders(t, response)
	}
	nested := performRequest(handler, http.MethodGet, publicPath+"nested/", "", "", nil)
	if nested.Code != http.StatusOK || string(nested.Body.Bytes()) != "<h1>Nested page</h1>" {
		t.Errorf("nested directory status=%d body=%q", nested.Code, nested.Body.Bytes())
	}
	if got := nested.Header().Get("Content-Security-Policy"); got != "sandbox allow-scripts" {
		t.Errorf("nested HTML CSP=%q, want sandbox allow-scripts", got)
	}
	assertPublicArtifactHeaders(t, nested)

	source := performRequest(handler, http.MethodGet, path+"/source", token, "", nil)
	if source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), archive) {
		t.Fatalf("source status=%d; original ZIP bytes were not preserved", source.Code)
	}
	filePath := path + "/files/css/site.css"
	file := performRequest(handler, http.MethodGet, filePath, token, "", nil)
	if file.Code != http.StatusOK || !bytes.Equal(file.Body.Bytes(), []byte("body { color: blue }")) {
		t.Fatalf("authenticated file endpoint status=%d body=%q", file.Code, file.Body.Bytes())
	}
	if got := performRequest(handler, http.MethodGet, filePath, "", "", nil); got.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated file endpoint status=%d, want %d", got.Code, http.StatusUnauthorized)
	}
	if got := performRequest(handler, http.MethodGet, path+"/files/invalid:name", token, "", nil); got.Code != http.StatusBadRequest {
		t.Errorf("invalid file path status=%d, want %d", got.Code, http.StatusBadRequest)
	}
}

func TestInvalidZIPDoesNotPublishOrReplaceArtifact(t *testing.T) {
	const token = "zip-validation-secret"
	handler, repository := newTestService(t, token)
	path := "/api/v1/projects/demo/artifacts/site"
	invalid := performRequest(handler, http.MethodPost, path, token, "application/zip", []byte("not a ZIP"))
	assertAPIError(t, invalid, http.StatusBadRequest, "invalid_archive")
	projects, err := repository.ListProjects()
	if err != nil {
		t.Fatalf("list projects after invalid create: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("invalid archive published project directories: %v", projects)
	}

	original := makeTestZIP(t, map[string][]byte{"index.html": []byte("old complete site")})
	created := performRequest(handler, http.MethodPost, path, token, "application/zip", original)
	if created.Code != http.StatusCreated {
		t.Fatalf("create prior ZIP status=%d body=%s", created.Code, created.Body.String())
	}
	failedUpdate := performRequest(handler, http.MethodPut, path, token, "application/zip", []byte("invalid replacement"))
	assertAPIError(t, failedUpdate, http.StatusBadRequest, "invalid_archive")
	root := performRequest(handler, http.MethodGet, "/p/demo/site/", "", "", nil)
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), `src="/p/demo/site/?_aidrop_content=1"`) {
		t.Fatalf("prior site viewer after rejected update status=%d body=%q", root.Code, root.Body.Bytes())
	}
	framedRoot := performRequest(handler, http.MethodGet, "/p/demo/site/?_aidrop_content=1", "", "", nil)
	if framedRoot.Code != http.StatusOK || string(framedRoot.Body.Bytes()) != "old complete site" {
		t.Fatalf("prior site after rejected update status=%d body=%q", framedRoot.Code, framedRoot.Body.Bytes())
	}
	source := performRequest(handler, http.MethodGet, path+"/source", token, "", nil)
	if source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), original) {
		t.Fatalf("source after rejected update status=%d; prior ZIP was not retained", source.Code)
	}
}

func TestSensitiveZIPEntriesDoNotPublishOrReplaceArtifact(t *testing.T) {
	const token = "zip-sensitive-entry-secret"
	handler, repository := newTestService(t, token)
	blockedPaths := []string{".env", "site/.git/config", "keys/id_ed25519", "tls/server.key"}
	for index, blockedPath := range blockedPaths {
		path := fmt.Sprintf("/api/v1/projects/demo/artifacts/rejected-%d", index)
		archive := makeTestZIP(t, map[string][]byte{
			"index.html": []byte("must not publish"),
			blockedPath:  []byte("sensitive"),
		})
		response := performRequest(handler, http.MethodPost, path, token, "application/zip", archive)
		assertAPIError(t, response, http.StatusBadRequest, "invalid_archive")
	}
	projects, err := repository.ListProjects()
	if err != nil {
		t.Fatalf("list projects after rejected sensitive archives: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("rejected archives published projects: %v", projects)
	}

	path := "/api/v1/projects/demo/artifacts/site"
	original := makeTestZIP(t, map[string][]byte{
		"index.html":      []byte("old complete site"),
		".env.example":    []byte("PUBLIC_URL="),
		".gitignore":      []byte("node_modules/"),
		"certs/site.pem":  []byte("public certificate"),
		"keys/client.pub": []byte("public SSH key"),
	})
	created := performRequest(handler, http.MethodPost, path, token, "application/zip", original)
	if created.Code != http.StatusCreated {
		t.Fatalf("create ordinary ZIP status=%d body=%s", created.Code, created.Body.String())
	}
	for _, blockedPath := range []string{".env.production", "assets/.git/config", "keys/id_rsa", "tls/private.pem"} {
		archive := makeTestZIP(t, map[string][]byte{
			"index.html": []byte("replacement must not publish"),
			blockedPath:  []byte("sensitive"),
		})
		response := performRequest(handler, http.MethodPut, path, token, "application/zip", archive)
		assertAPIError(t, response, http.StatusBadRequest, "invalid_archive")
	}
	root := performRequest(handler, http.MethodGet, "/p/demo/site/?_aidrop_content=1", "", "", nil)
	if root.Code != http.StatusOK || string(root.Body.Bytes()) != "old complete site" {
		t.Fatalf("published site after rejected replacements status=%d body=%q", root.Code, root.Body.Bytes())
	}
	source := performRequest(handler, http.MethodGet, path+"/source", token, "", nil)
	if source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), original) {
		t.Fatalf("source after rejected replacements status=%d; prior ZIP was not retained", source.Code)
	}
}

func TestMarkdownPublishRenderSourceAndUpdate(t *testing.T) {
	const token = "markdown-integration-secret"
	handler, _ := newTestService(t, token)
	path := "/api/v1/projects/demo/artifacts/report"
	publicPath := "/p/demo/report/"
	original := []byte("# Release report\r\n\r\n| Area | Status |\r\n| --- | --- |\r\n| API | ready |\r\n\r\n[Docs](https://example.test/docs)\r\n\r\n- first\r\n- second\r\n\r\n```go\r\nfmt.Println(\"ok\")\r\n```\r\n\r\n```mermaid\r\nclassDiagram\r\n  class Artifact\r\n```\r\n")

	created := performRequest(handler, http.MethodPost, path, token, "text/markdown; charset=utf-8", original)
	if created.Code != http.StatusCreated {
		t.Fatalf("Markdown POST status=%d body=%s", created.Code, created.Body.String())
	}
	var createdMetadata storage.Metadata
	if err := json.Unmarshal(created.Body.Bytes(), &createdMetadata); err != nil {
		t.Fatalf("decode Markdown create response: %v", err)
	}
	if createdMetadata.Format != storage.FormatMarkdown || createdMetadata.PublicURL != publicPath {
		t.Fatalf("Markdown metadata=%+v, want stable URL %s", createdMetadata, publicPath)
	}

	view := performRequest(handler, http.MethodGet, publicPath, "", "", nil)
	if view.Code != http.StatusOK {
		t.Fatalf("public Markdown view status=%d body=%s", view.Code, view.Body.String())
	}
	assertPublicArtifactHeaders(t, view)
	head := performRequest(handler, http.MethodHead, publicPath, "", "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD public Markdown preview status=%d body length=%d, want 200 with no body", head.Code, head.Body.Len())
	}
	assertPublicArtifactHeaders(t, head)
	if got := view.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Markdown public Content-Type=%q, want text/html", got)
	}
	if got := view.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || !strings.Contains(got, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("Markdown public CSP=%q, want the restrictive policy with same-origin Mermaid and inline source-view scripts", got)
	}
	for _, want := range []string{
		"<h1>Release report</h1>",
		"<table>",
		"<a href=\"https://example.test/docs\">Docs</a>",
		"<ul>",
		"<pre><code class=\"language-go\">",
		"<pre><code class=\"language-mermaid\">",
		"src=\"/assets/mermaid.tiny.min.js\"",
		"id=\"copy-source\" type=\"button\">Copy source</button>",
		"<main class=\"document-card\">",
		"class=\"document-kind\">Markdown document</p>",
		"@media (max-width: 640px)",
	} {
		if !strings.Contains(view.Body.String(), want) {
			t.Errorf("public Markdown page does not contain %q", want)
		}
	}
	if strings.Contains(view.Body.String(), "cdn.jsdelivr.net") || strings.Contains(view.Body.String(), "unpkg.com") {
		t.Error("Markdown page refers to an external Mermaid service")
	}
	if got := view.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("Markdown CSP=%q, want restrictive service page policy", got)
	}
	if encoded := base64.StdEncoding.EncodeToString(original); !strings.Contains(view.Body.String(), `value="`+encoded+`"`) {
		t.Error("public source view does not contain exact source bytes")
	}
	source := performRequest(handler, http.MethodGet, path+"/source", token, "", nil)
	if source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), original) {
		t.Fatalf("original source response status=%d body=%q", source.Code, source.Body.Bytes())
	}

	updatedSource := []byte("# Updated report\n\nNew details.\n")
	updated := performRequest(handler, http.MethodPut, path, token, "text/markdown", updatedSource)
	if updated.Code != http.StatusOK {
		t.Fatalf("Markdown PUT status=%d body=%s", updated.Code, updated.Body.String())
	}
	var updatedMetadata storage.Metadata
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedMetadata); err != nil {
		t.Fatalf("decode Markdown update response: %v", err)
	}
	if updatedMetadata.PublicURL != createdMetadata.PublicURL || !updatedMetadata.CreatedAt.Equal(createdMetadata.CreatedAt) {
		t.Errorf("Markdown update changed artifact identity: created=%+v updated=%+v", createdMetadata, updatedMetadata)
	}
	view = performRequest(handler, http.MethodGet, publicPath, "", "", nil)
	if view.Code != http.StatusOK || !strings.Contains(view.Body.String(), "<h1>Updated report</h1>") || strings.Contains(view.Body.String(), "<h1>Release report</h1>") {
		t.Errorf("public Markdown page after update status=%d body=%s", view.Code, view.Body.String())
	}
	source = performRequest(handler, http.MethodGet, path+"/source", token, "", nil)
	if source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), updatedSource) {
		t.Fatalf("updated source response status=%d body=%q", source.Code, source.Body.Bytes())
	}
}

func TestMarkdownPlantUMLPublishUsesConditionalCDNFrame(t *testing.T) {
	const token = "plantuml-integration-secret"
	handler, _ := newTestService(t, token)
	path := "/api/v1/projects/demo/artifacts/plantuml"
	publicPath := "/p/demo/plantuml/"
	original := []byte("# Architecture\n\n```plantuml\n@startuml\nAlice -> Bob: request\nBob --> Alice: response\n@enduml\n```\n\n```puml\nnot valid PlantUML\n```\n")

	created := performRequest(handler, http.MethodPost, path, token, "text/markdown", original)
	if created.Code != http.StatusCreated {
		t.Fatalf("PlantUML Markdown POST status=%d body=%s", created.Code, created.Body.String())
	}
	view := performRequest(handler, http.MethodGet, publicPath, "", "", nil)
	if view.Code != http.StatusOK {
		t.Fatalf("PlantUML Markdown view status=%d body=%s", view.Code, view.Body.String())
	}
	csp := view.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'none'",
		"frame-src 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("PlantUML Markdown CSP=%q, missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "https://unpkg.com") {
		t.Error("PlantUML parent page CSP should keep CDN permissions inside the renderer frame")
	}
	for _, want := range []string{
		`frame.src = "/assets/plantuml-frame.html?v=8"`,
		`frame.setAttribute("sandbox", "allow-scripts")`,
		`<code class="language-plantuml">`,
		`<code class="language-puml">`,
		"not valid PlantUML",
		"View original Markdown source",
	} {
		if !strings.Contains(view.Body.String(), want) {
			t.Errorf("PlantUML public page does not contain %q", want)
		}
	}
	if strings.Contains(view.Body.String(), "plantuml-server") || strings.Contains(view.Body.String(), "www.plantuml.com/plantuml") {
		t.Error("PlantUML page uses a remote rendering service")
	}
	encoded := base64.StdEncoding.EncodeToString(original)
	if !strings.Contains(view.Body.String(), `value="`+encoded+`"`) {
		t.Error("PlantUML public page does not contain exact source bytes")
	}
	source := performRequest(handler, http.MethodGet, path+"/source", token, "", nil)
	if source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), original) {
		t.Fatalf("PlantUML source response status=%d body=%q", source.Code, source.Body.Bytes())
	}
}

func TestMarkdownRejectsInvalidUTF8(t *testing.T) {
	handler, repository := newTestService(t, "markdown-validation-secret")
	response := performRequest(handler, http.MethodPost, "/api/v1/projects/demo/artifacts/invalid", "markdown-validation-secret", "text/markdown", []byte{0xff, 0xfe})
	assertAPIError(t, response, http.StatusBadRequest, "invalid_markdown")
	projects, err := repository.ListProjects()
	if err != nil {
		t.Fatalf("list projects after invalid Markdown: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("invalid Markdown published projects: %v", projects)
	}
}

func TestProjectDiscoveryAndConfirmedDeletion(t *testing.T) {
	const token = "project-api-secret"
	handler, _ := newTestService(t, token)
	alphaSource := []byte("alpha report")
	betaSource := []byte("# beta report")
	alpha := performRequest(handler, http.MethodPost, "/api/v1/projects/alpha/artifacts/report", token, "text/html", alphaSource)
	beta := performRequest(handler, http.MethodPost, "/api/v1/projects/beta/artifacts/report", token, "text/markdown", betaSource)
	if alpha.Code != http.StatusCreated || beta.Code != http.StatusCreated {
		t.Fatalf("create responses alpha=%d beta=%d", alpha.Code, beta.Code)
	}

	if response := performRequest(handler, http.MethodGet, "/api/v1/projects", "", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated project listing status=%d, want %d", response.Code, http.StatusUnauthorized)
	}
	projects := performRequest(handler, http.MethodGet, "/api/v1/projects", token, "", nil)
	if projects.Code != http.StatusOK {
		t.Fatalf("project listing status=%d body=%s", projects.Code, projects.Body.String())
	}
	var projectSlugs []string
	if err := json.Unmarshal(projects.Body.Bytes(), &projectSlugs); err != nil {
		t.Fatalf("decode project listing: %v", err)
	}
	if want := []string{"alpha", "beta"}; !slices.Equal(projectSlugs, want) {
		t.Fatalf("project listing=%v, want %v", projectSlugs, want)
	}

	for _, project := range []struct {
		slug   string
		format storage.Format
		source []byte
	}{{slug: "alpha", format: storage.FormatHTML, source: alphaSource}, {slug: "beta", format: storage.FormatMarkdown, source: betaSource}} {
		response := performRequest(handler, http.MethodGet, "/api/v1/projects/"+project.slug+"/artifacts", token, "", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("artifact listing for %s status=%d body=%s", project.slug, response.Code, response.Body.String())
		}
		var artifacts []storage.Metadata
		if err := json.Unmarshal(response.Body.Bytes(), &artifacts); err != nil {
			t.Fatalf("decode %s artifact listing: %v", project.slug, err)
		}
		if len(artifacts) != 1 {
			t.Fatalf("%s artifacts=%+v, want only its project-scoped report", project.slug, artifacts)
		}
		metadata := artifacts[0]
		if metadata.Project != project.slug || metadata.Artifact != "report" || metadata.Format != project.format || metadata.Size != int64(len(project.source)) || metadata.CreatedAt.IsZero() || metadata.ModifiedAt.IsZero() || metadata.PublicURL != "/p/"+project.slug+"/report/" {
			t.Errorf("%s artifact metadata=%+v, missing project-scoped fields", project.slug, metadata)
		}
	}
	if missing := performRequest(handler, http.MethodGet, "/api/v1/projects/missing/artifacts", token, "", nil); missing.Code != http.StatusNotFound {
		t.Errorf("missing project artifact list status=%d, want %d", missing.Code, http.StatusNotFound)
	}

	for _, tt := range []struct {
		query string
		code  string
	}{
		{query: "", code: "confirmation_required"},
		{query: "?confirm=wrong", code: "confirmation_mismatch"},
		{query: "?confirm=alpha&confirm=alpha", code: "confirmation_mismatch"},
		{query: "?confirm=alpha&other=value", code: "confirmation_mismatch"},
	} {
		response := performRequest(handler, http.MethodDelete, "/api/v1/projects/alpha"+tt.query, token, "", nil)
		assertAPIError(t, response, http.StatusBadRequest, tt.code)
		if source := performRequest(handler, http.MethodGet, "/api/v1/projects/alpha/artifacts/report/source", token, "", nil); source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), alphaSource) {
			t.Fatalf("rejected deletion changed alpha project source: status=%d body=%q", source.Code, source.Body.Bytes())
		}
	}

	deleted := performRequest(handler, http.MethodDelete, "/api/v1/projects/alpha?confirm=alpha", token, "", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("confirmed project deletion status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if got := performRequest(handler, http.MethodGet, "/api/v1/projects/alpha/artifacts", token, "", nil); got.Code != http.StatusNotFound {
		t.Errorf("artifact list after project deletion status=%d, want %d", got.Code, http.StatusNotFound)
	}
	projects = performRequest(handler, http.MethodGet, "/api/v1/projects", token, "", nil)
	if err := json.Unmarshal(projects.Body.Bytes(), &projectSlugs); err != nil {
		t.Fatalf("decode projects after deletion: %v", err)
	}
	if want := []string{"beta"}; !slices.Equal(projectSlugs, want) {
		t.Errorf("projects after deletion=%v, want %v", projectSlugs, want)
	}
	if source := performRequest(handler, http.MethodGet, "/api/v1/projects/beta/artifacts/report/source", token, "", nil); source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), betaSource) {
		t.Errorf("unrelated beta artifact after deletion status=%d body=%q", source.Code, source.Body.Bytes())
	}
	recreated := performRequest(handler, http.MethodPost, "/api/v1/projects/alpha/artifacts/new", token, "text/html", []byte("recreated"))
	if recreated.Code != http.StatusCreated {
		t.Errorf("publish after project deletion status=%d body=%s", recreated.Code, recreated.Body.String())
	}
}

func TestPublicCatalogPagesListProjectsArtifactsAndEmptyStates(t *testing.T) {
	handler, _ := newTestService(t, "catalog-secret")
	emptyRoot := performRequest(handler, http.MethodGet, "/", "", "", nil)
	if emptyRoot.Code != http.StatusOK || !strings.Contains(emptyRoot.Body.String(), "No projects yet") {
		t.Fatalf("empty root page status=%d body=%s", emptyRoot.Code, emptyRoot.Body.String())
	}

	for _, upload := range []struct {
		project  string
		artifact string
		format   string
		body     []byte
	}{
		{project: "alpha", artifact: "home", format: "text/html", body: []byte("<h1>Home</h1>")},
		{project: "alpha", artifact: "report", format: "text/markdown", body: []byte("# Report")},
		{project: "beta", artifact: "site", format: "application/zip", body: makeTestZIP(t, map[string][]byte{"index.html": []byte("<h1>ZIP</h1>")})},
		{project: "empty", artifact: "temporary", format: "text/html", body: []byte("temporary")},
	} {
		response := performRequest(handler, http.MethodPost, "/api/v1/projects/"+upload.project+"/artifacts/"+upload.artifact, "catalog-secret", upload.format, upload.body)
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s/%s status=%d body=%s", upload.project, upload.artifact, response.Code, response.Body.String())
		}
	}
	if response := performRequest(handler, http.MethodDelete, "/api/v1/projects/empty/artifacts/temporary", "catalog-secret", "", nil); response.Code != http.StatusNoContent {
		t.Fatalf("delete temporary artifact status=%d body=%s", response.Code, response.Body.String())
	}

	root := performRequest(handler, http.MethodGet, "/", "", "", nil)
	if root.Code != http.StatusOK {
		t.Fatalf("root page status=%d body=%s", root.Code, root.Body.String())
	}
	for _, project := range []string{"alpha", "beta", "empty"} {
		if !strings.Contains(root.Body.String(), `href="/p/`+project+`/"`) || !strings.Contains(root.Body.String(), `class="project-card__title">`+project+"</span>") {
			t.Errorf("root page is missing project link for %q: %s", project, root.Body.String())
		}
	}
	if strings.Index(root.Body.String(), `class="project-card__title">alpha</span>`) > strings.Index(root.Body.String(), `class="project-card__title">beta</span>`) || strings.Index(root.Body.String(), `class="project-card__title">beta</span>`) > strings.Index(root.Body.String(), `class="project-card__title">empty</span>`) {
		t.Errorf("root page projects are not sorted: %s", root.Body.String())
	}

	alpha := performRequest(handler, http.MethodGet, "/p/alpha/", "", "", nil)
	if alpha.Code != http.StatusOK {
		t.Fatalf("alpha page status=%d body=%s", alpha.Code, alpha.Body.String())
	}
	for _, want := range []string{
		`href="/p/alpha/home/">home</a>`,
		`href="/p/alpha/report/">report</a>`,
		"text/html",
		"text/markdown",
		"Modified <time datetime=",
		"href=\"/\">AI Drop</a>",
		"aria-label=\"Breadcrumb\"",
		"class=\"artifact-card\"",
		".site-header { position: sticky; top: 0; z-index: 10;",
		"@media (max-width: 620px)",
	} {
		if !strings.Contains(alpha.Body.String(), want) {
			t.Errorf("alpha page does not contain %q: %s", want, alpha.Body.String())
		}
	}
	if strings.Contains(alpha.Body.String(), "<form") || strings.Contains(alpha.Body.String(), "<script") || strings.Contains(alpha.Body.String(), "textarea") {
		t.Errorf("catalog page unexpectedly contains a form, script, or editor control")
	}
	if !strings.Contains(alpha.Body.String(), "<style>") || alpha.Header().Get("Content-Security-Policy") == "" {
		t.Error("catalog page is missing its local styling or CSP")
	}

	beta := performRequest(handler, http.MethodGet, "/p/beta/", "", "", nil)
	if beta.Code != http.StatusOK || !strings.Contains(beta.Body.String(), "application/zip") || !strings.Contains(beta.Body.String(), `href="/p/beta/site/">site</a>`) {
		t.Errorf("beta page does not show ZIP artifact and direct link: status=%d body=%s", beta.Code, beta.Body.String())
	}
	empty := performRequest(handler, http.MethodGet, "/p/empty/", "", "", nil)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), "No artifacts yet") {
		t.Errorf("empty project page status=%d body=%s", empty.Code, empty.Body.String())
	}
	missing := performRequest(handler, http.MethodGet, "/p/missing/", "", "", nil)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "This project was not found") {
		t.Errorf("missing project page status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestCatalogTemplatesEscapeUntrustedValues(t *testing.T) {
	project := `"><script>alert(1)</script>`
	index, err := renderProjectIndex([]string{project})
	if err != nil {
		t.Fatalf("render project index: %v", err)
	}
	if strings.Contains(string(index), "<script>alert(1)</script>") || !strings.Contains(string(index), "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("project index did not escape catalog project value: %s", index)
	}

	artifactName := `"><img src=x onerror=alert(2)>`
	page, err := renderProjectPage(project, []storage.Metadata{{
		Project:    project,
		Artifact:   artifactName,
		Format:     storage.Format(`<script>alert(3)</script>`),
		PublicURL:  `javascript:alert(4)`,
		ModifiedAt: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
	}}, true)
	if err != nil {
		t.Fatalf("render project page: %v", err)
	}
	contents := string(page)
	for _, active := range []string{"<script>alert(1)</script>", "<img src=x onerror=", "<script>alert(3)</script>", `href="javascript:`} {
		if strings.Contains(contents, active) {
			t.Errorf("project page contains active catalog content %q: %s", active, contents)
		}
	}
	if !strings.Contains(contents, "&lt;img src=x onerror=alert(2)&gt;") || !strings.Contains(contents, "&lt;script&gt;alert(3)&lt;/script&gt;") {
		t.Errorf("project page did not display escaped catalog values: %s", contents)
	}
}

func makeTestZIP(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for path, contents := range files {
		entry, err := writer.Create(path)
		if err != nil {
			t.Fatalf("create ZIP entry %q: %v", path, err)
		}
		if _, err := entry.Write(contents); err != nil {
			t.Fatalf("write ZIP entry %q: %v", path, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP archive: %v", err)
	}
	return archive.Bytes()
}

func performRequest(handler http.Handler, method, path, token, contentType string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d body=%s; want %d", response.Code, response.Body.String(), status)
	}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Code != code || body.Message == "" {
		t.Errorf("error response=%+v, want code %q and a message", body, code)
	}
}
