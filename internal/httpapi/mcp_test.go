package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"aidrop/internal/config"
	"aidrop/internal/storage"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpTestToken = "mcp-integration-secret"

func TestMCPHTTPToolsShareRESTOperations(t *testing.T) {
	repository, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	handler := NewHandler(config.Config{BearerToken: mcpTestToken}, repository)
	endpoint := "http://localhost/mcp"
	session := connectTestMCPClient(t, handler, endpoint, mcpTestToken, "http://localhost")
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}
	wantTools := []string{"delete_artifact", "delete_project", "get_artifact", "list_artifacts", "list_projects", "publish_artifact", "read_artifact_file", "update_artifact"}
	gotTools := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		gotTools = append(gotTools, tool.Name)
	}
	if !reflect.DeepEqual(gotTools, wantTools) {
		t.Errorf("tools=%v, want %v", gotTools, wantTools)
	}

	nativeSession := connectTestMCPClient(t, handler, endpoint, mcpTestToken, "")
	defer nativeSession.Close()
	if _, err := nativeSession.ListTools(context.Background(), nil); err != nil {
		t.Errorf("list tools without Origin header: %v", err)
	}

	created := callMCPTool(t, session, "publish_artifact", map[string]any{
		"project": "mcp-demo", "artifact": "report", "format": "text/html", "source": "<h1>first</h1>",
	})
	createdData := structuredMap(t, created)
	if created.IsError || createdData["ok"] != true {
		t.Fatalf("publish_artifact result=%+v structured=%v", created, createdData)
	}
	metadata := nestedMap(t, createdData["metadata"])
	if metadata["project"] != "mcp-demo" || metadata["artifact"] != "report" || metadata["format"] != "text/html" || metadata["url"] != "/p/mcp-demo/report/" {
		t.Fatalf("created metadata=%v", metadata)
	}

	restMetadataResponse := performRequest(handler, http.MethodGet, "/api/v1/projects/mcp-demo/artifacts/report", mcpTestToken, "", nil)
	if restMetadataResponse.Code != http.StatusOK {
		t.Fatalf("REST metadata response status=%d body=%s", restMetadataResponse.Code, restMetadataResponse.Body.String())
	}
	var restMetadata storage.Metadata
	if err := json.Unmarshal(restMetadataResponse.Body.Bytes(), &restMetadata); err != nil {
		t.Fatalf("decode REST metadata: %v", err)
	}
	if restMetadata.Format != storage.FormatHTML || restMetadata.PublicURL != metadata["url"] || restMetadata.Size != int64(len("<h1>first</h1>")) {
		t.Errorf("REST metadata=%+v does not match MCP output %v", restMetadata, metadata)
	}

	projectsResponse := performRequest(handler, http.MethodGet, "/api/v1/projects", mcpTestToken, "", nil)
	var restProjects []string
	if err := json.Unmarshal(projectsResponse.Body.Bytes(), &restProjects); err != nil {
		t.Fatalf("decode REST projects: %v", err)
	}
	projects := callMCPTool(t, session, "list_projects", map[string]any{})
	projectsData := structuredMap(t, projects)
	if projects.IsError || !reflect.DeepEqual(projectsData["projects"], []any{"mcp-demo"}) || !reflect.DeepEqual(projectsData["projects"], toAnySlice(restProjects)) {
		t.Errorf("MCP projects=%v REST projects=%v", projectsData["projects"], restProjects)
	}

	artifactList := callMCPTool(t, session, "list_artifacts", map[string]any{"project": "mcp-demo"})
	artifactListData := structuredMap(t, artifactList)
	if artifactList.IsError || artifactListData["project"] != "mcp-demo" {
		t.Fatalf("list_artifacts result=%+v structured=%v", artifactList, artifactListData)
	}
	restArtifactList := performRequest(handler, http.MethodGet, "/api/v1/projects/mcp-demo/artifacts", mcpTestToken, "", nil)
	var restArtifacts []storage.Metadata
	if err := json.Unmarshal(restArtifactList.Body.Bytes(), &restArtifacts); err != nil {
		t.Fatalf("decode REST artifact list: %v", err)
	}
	if len(restArtifacts) != 1 || nestedMap(t, artifactListData["artifacts"].([]any)[0])["artifact"] != restArtifacts[0].Artifact {
		t.Errorf("MCP artifact list=%v REST artifacts=%+v", artifactListData["artifacts"], restArtifacts)
	}

	getResult := callMCPTool(t, session, "get_artifact", map[string]any{"project": "mcp-demo", "artifact": "report"})
	getData := structuredMap(t, getResult)
	if getResult.IsError || getData["source_inline"] != true || getData["source_text"] != "<h1>first</h1>" {
		t.Errorf("get_artifact result=%+v structured=%v", getResult, getData)
	}
	getMetadata := nestedMap(t, getData["metadata"])
	if getMetadata["project"] != restMetadata.Project || getMetadata["artifact"] != restMetadata.Artifact || getMetadata["url"] != restMetadata.PublicURL {
		t.Errorf("MCP retrieval metadata=%v does not match REST metadata %+v", getMetadata, restMetadata)
	}

	updatedSource := "# Updated report\n"
	updated := callMCPTool(t, session, "update_artifact", map[string]any{
		"project": "mcp-demo", "artifact": "report", "format": "text/markdown", "source": updatedSource,
	})
	updatedData := structuredMap(t, updated)
	updatedMetadata := nestedMap(t, updatedData["metadata"])
	if updated.IsError || updatedData["url"] != restMetadata.PublicURL || updatedMetadata["format"] != "text/markdown" {
		t.Errorf("update_artifact result=%+v structured=%v", updated, updatedData)
	}
	restSource := performRequest(handler, http.MethodGet, "/api/v1/projects/mcp-demo/artifacts/report/source", mcpTestToken, "", nil)
	if restSource.Code != http.StatusOK || !bytes.Equal(restSource.Body.Bytes(), []byte(updatedSource)) {
		t.Errorf("REST source after MCP update status=%d body=%q", restSource.Code, restSource.Body.Bytes())
	}

	conflict := callMCPTool(t, session, "publish_artifact", map[string]any{
		"project": "mcp-demo", "artifact": "report", "format": "text/markdown", "source": "duplicate",
	})
	if !conflict.IsError || nestedMap(t, structuredMap(t, conflict)["error"])["code"] != "conflict" {
		t.Errorf("duplicate MCP publish result=%+v structured=%v", conflict, structuredMap(t, conflict))
	}
	restConflict := performRequest(handler, http.MethodPost, "/api/v1/projects/mcp-demo/artifacts/report", mcpTestToken, "text/markdown", []byte("duplicate"))
	if restConflict.Code != http.StatusConflict {
		t.Errorf("duplicate REST publish status=%d, want %d", restConflict.Code, http.StatusConflict)
	}

	invalidSlug := callMCPTool(t, session, "list_artifacts", map[string]any{"project": "BadProject"})
	if !invalidSlug.IsError || nestedMap(t, structuredMap(t, invalidSlug)["error"])["code"] != "invalid_slug" {
		t.Errorf("invalid slug MCP result=%+v", invalidSlug)
	}
	invalidSlugREST := performRequest(handler, http.MethodGet, "/api/v1/projects/BadProject/artifacts", mcpTestToken, "", nil)
	var invalidSlugRESTBody map[string]any
	if err := json.Unmarshal(invalidSlugREST.Body.Bytes(), &invalidSlugRESTBody); err != nil {
		t.Fatalf("decode REST invalid slug error: %v", err)
	}
	if invalidSlugREST.Code != http.StatusBadRequest || invalidSlugRESTBody["code"] != nestedMap(t, structuredMap(t, invalidSlug)["error"])["code"] {
		t.Errorf("MCP invalid slug result=%v REST status=%d result=%v", structuredMap(t, invalidSlug), invalidSlugREST.Code, invalidSlugRESTBody)
	}
	zipHandoff := callMCPTool(t, session, "publish_artifact", map[string]any{
		"project": "mcp-demo", "artifact": "zip-site", "format": "application/zip", "source": "base64-or-text-is-not-accepted",
	})
	zipData := structuredMap(t, zipHandoff)
	zipREST := nestedMap(t, zipData["rest_handoff"])
	if !zipHandoff.IsError || nestedMap(t, zipData["error"])["code"] != "rest_handoff_required" || zipREST["method"] != http.MethodPost || zipREST["url"] != "/api/v1/projects/mcp-demo/artifacts/zip-site" {
		t.Errorf("ZIP handoff=%+v structured=%v", zipHandoff, zipData)
	}

	archive := makeTestZIP(t, map[string][]byte{
		"index.html":      []byte("<h1>File site</h1>"),
		"css/site.css":    []byte("body { color: teal }"),
		"images/logo.png": []byte{0x89, 'P', 'N', 'G', 0x00},
	})
	zipCreated := performRequest(handler, http.MethodPost, "/api/v1/projects/file-demo/artifacts/site", mcpTestToken, "application/zip", archive)
	if zipCreated.Code != http.StatusCreated {
		t.Fatalf("create ZIP for MCP file reads status=%d body=%s", zipCreated.Code, zipCreated.Body.String())
	}
	fileRead := callMCPTool(t, session, "read_artifact_file", map[string]any{
		"project": "file-demo", "artifact": "site", "path": "css/site.css",
	})
	fileData := structuredMap(t, fileRead)
	if fileRead.IsError || fileData["inline"] != true || fileData["text"] != "body { color: teal }" {
		t.Errorf("read text file result=%+v structured=%v", fileRead, fileData)
	}
	binaryRead := callMCPTool(t, session, "read_artifact_file", map[string]any{
		"project": "file-demo", "artifact": "site", "path": "images/logo.png",
	})
	binaryData := structuredMap(t, binaryRead)
	binaryHandoff := nestedMap(t, binaryData["rest_handoff"])
	if binaryRead.IsError || binaryData["inline"] != false || binaryHandoff["url"] != "/api/v1/projects/file-demo/artifacts/site/files/images/logo.png" {
		t.Errorf("binary file REST handoff=%+v structured=%v", binaryRead, binaryData)
	}
	unsafeRead := callMCPTool(t, session, "read_artifact_file", map[string]any{
		"project": "file-demo", "artifact": "site", "path": "../source",
	})
	if !unsafeRead.IsError || nestedMap(t, structuredMap(t, unsafeRead)["error"])["code"] != "invalid_site_path" {
		t.Errorf("unsafe MCP file path result=%+v", unsafeRead)
	}

	largeSource := strings.Repeat("x", maxMCPInlineBytes+1)
	largeCreate := performRequest(handler, http.MethodPost, "/api/v1/projects/mcp-demo/artifacts/large", mcpTestToken, "text/html", []byte(largeSource))
	if largeCreate.Code != http.StatusCreated {
		t.Fatalf("create large artifact for handoff status=%d body=%s", largeCreate.Code, largeCreate.Body.String())
	}
	largeGet := callMCPTool(t, session, "get_artifact", map[string]any{"project": "mcp-demo", "artifact": "large"})
	largeData := structuredMap(t, largeGet)
	largeHandoff := nestedMap(t, largeData["source_rest_handoff"])
	if largeGet.IsError || largeData["source_inline"] != false || largeHandoff["url"] != "/api/v1/projects/mcp-demo/artifacts/large/source" {
		t.Errorf("large artifact handoff=%+v structured=%v", largeGet, largeData)
	}

	deleteArtifact := callMCPTool(t, session, "delete_artifact", map[string]any{"project": "mcp-demo", "artifact": "report"})
	if deleteArtifact.IsError || structuredMap(t, deleteArtifact)["deleted"] != true {
		t.Errorf("delete_artifact result=%+v", deleteArtifact)
	}
	if missing := performRequest(handler, http.MethodGet, "/api/v1/projects/mcp-demo/artifacts/report", mcpTestToken, "", nil); missing.Code != http.StatusNotFound {
		t.Errorf("REST lookup after MCP delete status=%d, want 404", missing.Code)
	}

	wrongConfirm := callMCPTool(t, session, "delete_project", map[string]any{"project": "file-demo", "confirm": "wrong"})
	if !wrongConfirm.IsError || nestedMap(t, structuredMap(t, wrongConfirm)["error"])["code"] != "confirmation_mismatch" {
		t.Errorf("mismatched MCP project confirmation result=%+v", wrongConfirm)
	}
	deleteProject := callMCPTool(t, session, "delete_project", map[string]any{"project": "file-demo", "confirm": "file-demo"})
	if deleteProject.IsError || structuredMap(t, deleteProject)["deleted"] != true {
		t.Errorf("delete_project result=%+v", deleteProject)
	}
	if missing := performRequest(handler, http.MethodGet, "/api/v1/projects/file-demo/artifacts", mcpTestToken, "", nil); missing.Code != http.StatusNotFound {
		t.Errorf("REST project lookup after MCP delete status=%d, want 404", missing.Code)
	}

	createdAt := restMetadata.CreatedAt
	if updatedMetadata["created_at"] != createdAt.Format(time.RFC3339Nano) {
		t.Errorf("updated artifact creation timestamp=%v, want %s", updatedMetadata["created_at"], createdAt.Format(time.RFC3339Nano))
	}
}

func TestMCPHTTPRequiresBearerAndRejectsInvalidOrigin(t *testing.T) {
	handler, _ := newTestService(t, mcpTestToken)
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	request, err := http.NewRequest(http.MethodPost, "http://localhost/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create unauthenticated request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := serveMCPTestRequest(handler, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer token status=%d, want %d", response.Code, http.StatusUnauthorized)
	}

	request, err = http.NewRequest(http.MethodPost, "http://localhost/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create invalid-origin request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+mcpTestToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Origin", "https://attacker.example")
	response = serveMCPTestRequest(handler, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("invalid origin status=%d, want %d", response.Code, http.StatusForbidden)
	}

	request, err = http.NewRequest(http.MethodPost, "http://localhost/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create invalid-token request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer wrong-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Origin", "http://localhost")
	response = serveMCPTestRequest(handler, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status=%d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestMCPHTTPAllowsConfiguredOriginBehindTLSProxy(t *testing.T) {
	repository, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	handler := NewHandler(config.Config{
		BearerToken:    mcpTestToken,
		AllowedOrigins: []string{"https://aidrop.example"},
	}, repository)
	session := connectTestMCPClient(t, handler, "http://aidrop-internal/mcp", mcpTestToken, "https://aidrop.example")
	defer session.Close()
	if _, err := session.ListTools(context.Background(), nil); err != nil {
		t.Fatalf("connect from configured public Origin through internal HTTP: %v", err)
	}

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	unlisted := httptest.NewRequest(http.MethodPost, "http://aidrop-internal/mcp", bytes.NewReader(body))
	unlisted.Header.Set("Authorization", "Bearer "+mcpTestToken)
	unlisted.Header.Set("Content-Type", "application/json")
	unlisted.Header.Set("Accept", "application/json, text/event-stream")
	unlisted.Header.Set("Origin", "https://unlisted.example")
	if response := serveMCPTestRequest(handler, unlisted); response.Code != http.StatusForbidden {
		t.Errorf("unlisted Origin status=%d, want %d", response.Code, http.StatusForbidden)
	}

	duplicate := httptest.NewRequest(http.MethodPost, "http://aidrop-internal/mcp", bytes.NewReader(body))
	duplicate.Header.Set("Authorization", "Bearer "+mcpTestToken)
	duplicate.Header.Set("Content-Type", "application/json")
	duplicate.Header.Set("Accept", "application/json, text/event-stream")
	duplicate.Header.Add("Origin", "https://aidrop.example")
	duplicate.Header.Add("Origin", "https://unlisted.example")
	if response := serveMCPTestRequest(handler, duplicate); response.Code != http.StatusForbidden {
		t.Errorf("multiple Origin headers status=%d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestCrossInterfaceReleaseWorkflowAcrossFormats(t *testing.T) {
	const project = "release-check"
	const token = "release-check-secret"
	repository, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	handler := NewHandler(config.Config{BearerToken: token}, repository)
	session := connectTestMCPClient(t, handler, "http://localhost/mcp", token, "")
	defer session.Close()

	// Create through REST, update through MCP, then read through the public and REST views.
	htmlPath := "/api/v1/projects/" + project + "/artifacts/guide"
	htmlCreate := performRequest(handler, http.MethodPost, htmlPath, token, "text/html", []byte("<h1>release guide v1</h1>"))
	var htmlCreated storage.Metadata
	if htmlCreate.Code != http.StatusCreated || json.Unmarshal(htmlCreate.Body.Bytes(), &htmlCreated) != nil {
		t.Fatalf("create HTML: status=%d body=%s", htmlCreate.Code, htmlCreate.Body.String())
	}
	htmlUpdatedSource := "<h1>release guide v2</h1>"
	htmlUpdate := callMCPTool(t, session, "update_artifact", map[string]any{
		"project": project, "artifact": "guide", "format": "text/html", "source": htmlUpdatedSource,
	})
	htmlUpdateData := structuredMap(t, htmlUpdate)
	if htmlUpdate.IsError || htmlUpdateData["url"] != htmlCreated.PublicURL {
		t.Fatalf("MCP HTML update=%+v structured=%v", htmlUpdate, htmlUpdateData)
	}
	var htmlUpdated storage.Metadata
	if err := json.Unmarshal(byteMustJSON(t, htmlUpdateData["metadata"]), &htmlUpdated); err != nil {
		t.Fatalf("decode updated HTML metadata: %v", err)
	}
	if htmlUpdated.PublicURL != htmlCreated.PublicURL || !htmlUpdated.CreatedAt.Equal(htmlCreated.CreatedAt) {
		t.Errorf("HTML update changed stable identity: before=%+v after=%+v", htmlCreated, htmlUpdated)
	}
	if viewed := performRequest(handler, http.MethodGet, htmlCreated.PublicURL, "", "", nil); viewed.Code != http.StatusOK || !strings.Contains(viewed.Body.String(), `href="/p/release-check/">release-check</a>`) {
		t.Errorf("updated HTML viewer status=%d body=%q", viewed.Code, viewed.Body.String())
	}
	if viewed := performRequest(handler, http.MethodGet, htmlCreated.PublicURL+"?_aidrop_content=1", "", "", nil); viewed.Code != http.StatusOK || viewed.Body.String() != htmlUpdatedSource {
		t.Errorf("updated HTML frame content status=%d body=%q", viewed.Code, viewed.Body.String())
	}
	if source := performRequest(handler, http.MethodGet, htmlPath+"/source", token, "", nil); source.Code != http.StatusOK || source.Body.String() != htmlUpdatedSource {
		t.Errorf("updated HTML source status=%d body=%q", source.Code, source.Body.String())
	}

	// Publish and replace a ZIP site through REST; read its extracted file through MCP.
	zipPath := "/api/v1/projects/" + project + "/artifacts/site"
	zipV1 := makeTestZIP(t, map[string][]byte{"index.html": []byte("<h1>ZIP site v1</h1>")})
	zipCreate := performRequest(handler, http.MethodPost, zipPath, token, "application/zip", zipV1)
	var zipCreated storage.Metadata
	if zipCreate.Code != http.StatusCreated || json.Unmarshal(zipCreate.Body.Bytes(), &zipCreated) != nil {
		t.Fatalf("create ZIP: status=%d body=%s", zipCreate.Code, zipCreate.Body.String())
	}
	zipRead := callMCPTool(t, session, "read_artifact_file", map[string]any{
		"project": project, "artifact": "site", "path": "index.html",
	})
	zipReadData := structuredMap(t, zipRead)
	if zipRead.IsError || zipReadData["inline"] != true || zipReadData["text"] != "<h1>ZIP site v1</h1>" {
		t.Fatalf("read ZIP file through MCP=%+v structured=%v", zipRead, zipReadData)
	}
	zipV2 := makeTestZIP(t, map[string][]byte{"index.html": []byte("<h1>ZIP site v2</h1>")})
	zipUpdate := performRequest(handler, http.MethodPut, zipPath, token, "application/zip", zipV2)
	var zipUpdated storage.Metadata
	if zipUpdate.Code != http.StatusOK || json.Unmarshal(zipUpdate.Body.Bytes(), &zipUpdated) != nil {
		t.Fatalf("update ZIP: status=%d body=%s", zipUpdate.Code, zipUpdate.Body.String())
	}
	if zipUpdated.PublicURL != zipCreated.PublicURL || !zipUpdated.CreatedAt.Equal(zipCreated.CreatedAt) {
		t.Errorf("ZIP update changed stable identity: before=%+v after=%+v", zipCreated, zipUpdated)
	}
	if viewed := performRequest(handler, http.MethodGet, zipUpdated.PublicURL, "", "", nil); viewed.Code != http.StatusOK || !strings.Contains(viewed.Body.String(), `src="/_aidrop/content/release-check/site/"`) {
		t.Errorf("updated ZIP viewer status=%d body=%q", viewed.Code, viewed.Body.String())
	}
	if viewed := performRequest(handler, http.MethodGet, zipUpdated.PublicURL+"?_aidrop_content=1", "", "", nil); viewed.Code != http.StatusOK || !strings.Contains(viewed.Body.String(), "ZIP site v2") {
		t.Errorf("updated ZIP frame content status=%d body=%q", viewed.Code, viewed.Body.String())
	}
	zipRetrieved := callMCPTool(t, session, "get_artifact", map[string]any{"project": project, "artifact": "site"})
	zipRetrievedData := structuredMap(t, zipRetrieved)
	if zipRetrieved.IsError || zipRetrievedData["source_inline"] != false || nestedMap(t, zipRetrievedData["source_rest_handoff"])["url"] != zipPath+"/source" {
		t.Errorf("ZIP source REST handoff=%+v structured=%v", zipRetrieved, zipRetrievedData)
	}

	// Publish Markdown through MCP, replace through REST, and verify rendered and source views.
	markdownV1 := "# Release notes v1\n"
	markdownCreate := callMCPTool(t, session, "publish_artifact", map[string]any{
		"project": project, "artifact": "notes", "format": "text/markdown", "source": markdownV1,
	})
	markdownCreateData := structuredMap(t, markdownCreate)
	if markdownCreate.IsError {
		t.Fatalf("publish Markdown through MCP=%+v structured=%v", markdownCreate, markdownCreateData)
	}
	var markdownCreated storage.Metadata
	if err := json.Unmarshal(byteMustJSON(t, nestedMap(t, markdownCreateData["metadata"])), &markdownCreated); err != nil {
		t.Fatalf("decode created Markdown metadata: %v", err)
	}
	markdownPath := "/api/v1/projects/" + project + "/artifacts/notes"
	markdownV2 := []byte("# Release notes v2\n")
	markdownUpdate := performRequest(handler, http.MethodPut, markdownPath, token, "text/markdown", markdownV2)
	var markdownUpdated storage.Metadata
	if markdownUpdate.Code != http.StatusOK || json.Unmarshal(markdownUpdate.Body.Bytes(), &markdownUpdated) != nil {
		t.Fatalf("update Markdown: status=%d body=%s", markdownUpdate.Code, markdownUpdate.Body.String())
	}
	if markdownUpdated.PublicURL != markdownCreated.PublicURL || !markdownUpdated.CreatedAt.Equal(markdownCreated.CreatedAt) {
		t.Errorf("Markdown update changed stable identity: before=%+v after=%+v", markdownCreated, markdownUpdated)
	}
	if viewed := performRequest(handler, http.MethodGet, markdownUpdated.PublicURL, "", "", nil); viewed.Code != http.StatusOK || !strings.Contains(viewed.Body.String(), "Release notes v2") {
		t.Errorf("updated Markdown view status=%d body=%q", viewed.Code, viewed.Body.String())
	}
	if source := performRequest(handler, http.MethodGet, markdownPath+"/source", token, "", nil); source.Code != http.StatusOK || !bytes.Equal(source.Body.Bytes(), markdownV2) {
		t.Errorf("updated Markdown source status=%d body=%q", source.Code, source.Body.Bytes())
	}

	// Compare project listings across REST and MCP, then remove the project through MCP.
	restProjectsResponse := performRequest(handler, http.MethodGet, "/api/v1/projects", token, "", nil)
	var restProjects []string
	if err := json.Unmarshal(restProjectsResponse.Body.Bytes(), &restProjects); err != nil {
		t.Fatalf("decode REST project list: %v", err)
	}
	mcpProjects := callMCPTool(t, session, "list_projects", map[string]any{})
	if mcpProjects.IsError || !reflect.DeepEqual(structuredMap(t, mcpProjects)["projects"], toAnySlice(restProjects)) {
		t.Errorf("MCP projects=%v REST projects=%v", structuredMap(t, mcpProjects)["projects"], restProjects)
	}
	restArtifactsResponse := performRequest(handler, http.MethodGet, "/api/v1/projects/"+project+"/artifacts", token, "", nil)
	var restArtifacts []storage.Metadata
	if err := json.Unmarshal(restArtifactsResponse.Body.Bytes(), &restArtifacts); err != nil {
		t.Fatalf("decode REST artifact list: %v", err)
	}
	mcpArtifacts := callMCPTool(t, session, "list_artifacts", map[string]any{"project": project})
	mcpArtifactValues := structuredMap(t, mcpArtifacts)["artifacts"].([]any)
	if mcpArtifacts.IsError || len(restArtifacts) != len(mcpArtifactValues) || len(restArtifacts) != 3 {
		t.Fatalf("MCP artifact list=%v REST artifact list=%+v", mcpArtifactValues, restArtifacts)
	}
	for i, metadataValue := range mcpArtifactValues {
		metadata := nestedMap(t, metadataValue)
		if metadata["artifact"] != restArtifacts[i].Artifact || metadata["format"] != string(restArtifacts[i].Format) || metadata["url"] != restArtifacts[i].PublicURL {
			t.Errorf("MCP artifact %d=%v does not match REST metadata %+v", i, metadata, restArtifacts[i])
		}
	}
	deleted := callMCPTool(t, session, "delete_project", map[string]any{"project": project, "confirm": project})
	if deleted.IsError || structuredMap(t, deleted)["deleted"] != true {
		t.Fatalf("delete project through MCP=%+v structured=%v", deleted, structuredMap(t, deleted))
	}
	if missing := performRequest(handler, http.MethodGet, "/api/v1/projects/"+project+"/artifacts", token, "", nil); missing.Code != http.StatusNotFound {
		t.Errorf("REST project lookup after MCP delete status=%d, want 404", missing.Code)
	}
}

func byteMustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON value: %v", err)
	}
	return encoded
}

func connectTestMCPClient(t *testing.T, handler http.Handler, endpoint, token, origin string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "aidrop-test", Version: "1.0.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: inProcessRoundTripper{handler: handler, token: token, origin: origin}},
		DisableStandaloneSSE: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	return session
}

type inProcessRoundTripper struct {
	handler http.Handler
	token   string
	origin  string
}

func (b inProcessRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+b.token)
	if b.origin != "" {
		clone.Header.Set("Origin", b.origin)
	}
	response := httptest.NewRecorder()
	b.handler.ServeHTTP(response, clone)
	return response.Result(), nil
}

func serveMCPTestRequest(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func callMCPTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("call MCP tool %s: %v", name, err)
	}
	return result
}

func structuredMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal MCP structured result: %v", err)
	}
	var structured map[string]any
	if err := json.Unmarshal(encoded, &structured); err != nil {
		t.Fatalf("decode MCP structured result: %v", err)
	}
	return structured
}

func nestedMap(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal nested MCP result: %v", err)
	}
	var nested map[string]any
	if err := json.Unmarshal(encoded, &nested); err != nil {
		t.Fatalf("decode nested MCP result: %v", err)
	}
	return nested
}

func toAnySlice(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
