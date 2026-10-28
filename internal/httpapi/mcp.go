package httpapi

import (
	"context"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"aidrop/internal/config"
	"aidrop/internal/storage"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxMCPInlineBytes = 1 << 20

type publishArtifactInput struct {
	Project  string `json:"project" jsonschema:"project slug"`
	Artifact string `json:"artifact" jsonschema:"artifact slug"`
	Format   string `json:"format" jsonschema:"text/html or text/markdown; use the REST upload route for application/zip"`
	Source   string `json:"source" jsonschema:"raw UTF-8 HTML or Markdown body, at most 1 MiB"`
}

type artifactInput struct {
	Project  string `json:"project" jsonschema:"project slug"`
	Artifact string `json:"artifact" jsonschema:"artifact slug"`
}

type projectInput struct {
	Project string `json:"project" jsonschema:"project slug"`
}

type readArtifactFileInput struct {
	Project  string `json:"project" jsonschema:"project slug"`
	Artifact string `json:"artifact" jsonschema:"artifact slug"`
	Path     string `json:"path" jsonschema:"relative path of an extracted ZIP site file"`
}

type deleteProjectInput struct {
	Project string `json:"project" jsonschema:"project slug"`
	Confirm string `json:"confirm" jsonschema:"must exactly match the project slug"`
}

type listProjectsOutput struct {
	OK       bool     `json:"ok"`
	Projects []string `json:"projects"`
}

func newMCPHTTPHandler(s *server) http.Handler {
	implementation := &mcp.Implementation{Name: "aidrop", Version: "1.0.0"}
	server := mcp.NewServer(implementation, nil)
	registerMCPTools(server, s.repository)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          32 << 20,
		PropagateRequestCancellation: true,
	})
}

func validRequestOrigin(r *http.Request, allowedOrigins []string) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	originKey, err := config.NormalizeOrigin(origins[0])
	if err != nil {
		return false
	}
	for _, allowedOrigin := range allowedOrigins {
		allowedKey, err := config.NormalizeOrigin(allowedOrigin)
		if err == nil && originKey == allowedKey {
			return true
		}
	}

	origin, err := url.Parse(origins[0])
	if err != nil {
		return false
	}
	originScheme := "http"
	if r.TLS != nil {
		originScheme = "https"
	}
	requestOrigin, err := config.NormalizeOrigin(originScheme + "://" + r.Host)
	if err != nil || originKey != requestOrigin {
		return false
	}
	host := origin.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func registerMCPTools(server *mcp.Server, repository *storage.Repository) {
	destructive := true
	mcp.AddTool(server, &mcp.Tool{
		Name:        "publish_artifact",
		Title:       "Publish artifact",
		Description: "Create an HTML or Markdown artifact with a stable public URL. Publishing the first artifact for a project slug creates that project; use the same project slug and a different artifact slug to add pages. There is no separate create_project tool. Text bodies up to 1 MiB can be sent inline. Upload ZIP or larger bodies through the authenticated REST route returned by this tool.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input publishArtifactInput) (*mcp.CallToolResult, any, error) {
		return publishMCPArtifact(repository, input, false)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_artifact",
		Title:       "Update artifact",
		Description: "Replace an existing HTML or Markdown artifact while preserving its public URL and creation time. Text bodies up to 1 MiB can be sent inline; use the authenticated REST route for ZIP or larger bodies.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input publishArtifactInput) (*mcp.CallToolResult, any, error) {
		return publishMCPArtifact(repository, input, true)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_projects",
		Title:       "List projects",
		Description: "List all project slugs in stable sorted order.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		projects, err := repository.ListProjects()
		if err != nil {
			return toolStorageError(err), nil, nil
		}
		out := listProjectsOutput{OK: true, Projects: projects}
		return mcpSuccess("Listed projects.", out), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_artifacts",
		Title:       "List project artifacts",
		Description: "List a project's artifacts with format, timestamps, size, and stable public URLs.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input projectInput) (*mcp.CallToolResult, any, error) {
		artifacts, err := repository.ListArtifacts(input.Project)
		if err != nil {
			return toolStorageError(err), nil, nil
		}
		return mcpSuccess("Listed artifacts for project "+input.Project+".", map[string]any{
			"ok":        true,
			"project":   input.Project,
			"artifacts": artifacts,
		}), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_artifact",
		Title:       "Get artifact",
		Description: "Get artifact metadata and inline HTML/Markdown source up to 1 MiB. ZIP, binary, invalid UTF-8, and larger source use the authenticated REST source route included in the result.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input artifactInput) (*mcp.CallToolResult, any, error) {
		metadata, source, err := repository.Get(input.Project, input.Artifact)
		if err != nil {
			return toolStorageError(err), nil, nil
		}
		out := map[string]any{
			"ok":                  true,
			"metadata":            metadata,
			"source_inline":       false,
			"source_rest_handoff": sourceHandoff(input.Project, input.Artifact),
		}
		if (metadata.Format == storage.FormatHTML || metadata.Format == storage.FormatMarkdown) && len(source) <= maxMCPInlineBytes && utf8.Valid(source) {
			out["source_inline"] = true
			out["source_text"] = string(source)
			delete(out, "source_rest_handoff")
		}
		return mcpSuccess("Retrieved artifact metadata and source availability.", out), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_artifact_file",
		Title:       "Read artifact file",
		Description: "Read an extracted ZIP site file. Text files up to 1 MiB are returned inline; binary, non-text, and larger files use the authenticated REST file route in the result.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input readArtifactFileInput) (*mcp.CallToolResult, any, error) {
		contents, err := repository.GetSiteFile(input.Project, input.Artifact, input.Path)
		if err != nil {
			return toolStorageError(err), nil, nil
		}
		contentType := contentTypeForSitePath(input.Path)
		out := map[string]any{
			"ok":           true,
			"project":      input.Project,
			"artifact":     input.Artifact,
			"path":         input.Path,
			"content_type": contentType,
			"inline":       false,
			"rest_handoff": fileHandoff(input.Project, input.Artifact, input.Path),
		}
		if len(contents) <= maxMCPInlineBytes && isTextualContentType(contentType) && utf8.Valid(contents) {
			out["inline"] = true
			out["text"] = string(contents)
			delete(out, "rest_handoff")
		}
		return mcpSuccess("Read artifact file or provided REST handoff.", out), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_artifact",
		Title:       "Delete artifact",
		Description: "Delete one artifact from a project. This action is permanent.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input artifactInput) (*mcp.CallToolResult, any, error) {
		if err := repository.DeleteArtifact(input.Project, input.Artifact); err != nil {
			return toolStorageError(err), nil, nil
		}
		return mcpSuccess("Deleted artifact.", map[string]any{
			"ok":       true,
			"project":  input.Project,
			"artifact": input.Artifact,
			"deleted":  true,
		}), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_project",
		Title:       "Delete project",
		Description: "Permanently delete a project and every artifact in it. The confirm argument must exactly match the project slug.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input deleteProjectInput) (*mcp.CallToolResult, any, error) {
		if err := repository.DeleteProject(input.Project, input.Confirm); err != nil {
			return toolStorageError(err), nil, nil
		}
		return mcpSuccess("Deleted project.", map[string]any{
			"ok":      true,
			"project": input.Project,
			"deleted": true,
		}), nil, nil
	})
}

func publishMCPArtifact(repository *storage.Repository, input publishArtifactInput, replace bool) (*mcp.CallToolResult, any, error) {
	if err := storage.ValidateSlug(input.Project); err != nil {
		return toolStorageError(err), nil, nil
	}
	if err := storage.ValidateSlug(input.Artifact); err != nil {
		return toolStorageError(err), nil, nil
	}
	format := storage.Format(input.Format)
	if format == storage.FormatZIP {
		return mcpHandoffError("rest_handoff_required", "ZIP uploads must use the authenticated REST route.", uploadHandoff(input.Project, input.Artifact, format, replace)), nil, nil
	}
	if format != storage.FormatHTML && format != storage.FormatMarkdown {
		return toolStorageError(storage.ErrInvalidFormat), nil, nil
	}
	if len(input.Source) == 0 {
		return toolError("empty_payload", "Artifact source must not be empty."), nil, nil
	}
	if len(input.Source) > maxMCPInlineBytes {
		return mcpHandoffError("rest_handoff_required", "Text bodies larger than 1 MiB must use the authenticated REST route.", uploadHandoff(input.Project, input.Artifact, format, replace)), nil, nil
	}
	if format == storage.FormatMarkdown && !utf8.ValidString(input.Source) {
		return toolError("invalid_markdown", "Markdown source must be valid UTF-8."), nil, nil
	}
	inputArtifact := storage.NewArtifact{
		Project:  input.Project,
		Artifact: input.Artifact,
		Format:   format,
		Source:   []byte(input.Source),
	}
	var metadata storage.Metadata
	var err error
	if replace {
		metadata, err = repository.Replace(inputArtifact)
	} else {
		metadata, err = repository.Create(inputArtifact)
	}
	if err != nil {
		return toolStorageError(err), nil, nil
	}
	verb := "Published"
	if replace {
		verb = "Updated"
	}
	return mcpSuccess(verb+" artifact at "+metadata.PublicURL+".", map[string]any{
		"ok":       true,
		"metadata": metadata,
		"url":      metadata.PublicURL,
	}), nil, nil
}

func mcpSuccess(message string, structured any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: structured,
	}
}

func toolError(code, message string) *mcp.CallToolResult {
	return mcpErrorResult(code, message, nil)
}

func mcpHandoffError(code, message string, handoff map[string]any) *mcp.CallToolResult {
	return mcpErrorResult(code, message, handoff)
}

func mcpErrorResult(code, message string, handoff map[string]any) *mcp.CallToolResult {
	errorValue := map[string]any{"code": code, "message": message}
	structured := map[string]any{"ok": false, "error": errorValue}
	if handoff != nil {
		structured["rest_handoff"] = handoff
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: structured,
		IsError:           true,
	}
}

func toolStorageError(err error) *mcp.CallToolResult {
	switch {
	case errors.Is(err, storage.ErrInvalidSlug):
		return toolError("invalid_slug", "Project and artifact slugs must be lowercase kebab-case.")
	case errors.Is(err, storage.ErrInvalidSitePath):
		return toolError("invalid_site_path", "The site file path is invalid.")
	case errors.Is(err, storage.ErrInvalidFormat):
		return toolError("invalid_format", "The artifact format is not supported inline.")
	case errors.Is(err, storage.ErrConfirmationRequired):
		return toolError("confirmation_required", "The confirm value is required and must exactly match the project slug.")
	case errors.Is(err, storage.ErrConfirmationMismatch):
		return toolError("confirmation_mismatch", "The confirm value must exactly match the project slug.")
	case errors.Is(err, storage.ErrConflict):
		return toolError("conflict", "An artifact with that project and artifact slug already exists.")
	case errors.Is(err, storage.ErrNotFound):
		return toolError("not_found", "The requested project, artifact, or file was not found.")
	case errors.Is(err, storage.ErrCorrupt):
		return toolError("corrupt_artifact", "Stored artifact data is unavailable or corrupt.")
	default:
		return toolError("internal_error", "The artifact operation failed internally.")
	}
}

func sourceHandoff(project, artifact string) map[string]any {
	return restHandoff(http.MethodGet, artifactBasePath(project, artifact)+"/source", "application/octet-stream")
}

func uploadHandoff(project, artifact string, format storage.Format, replace bool) map[string]any {
	method := http.MethodPost
	if replace {
		method = http.MethodPut
	}
	return restHandoff(method, artifactBasePath(project, artifact), string(format))
}

func fileHandoff(project, artifact, sitePath string) map[string]any {
	return restHandoff(http.MethodGet, artifactBasePath(project, artifact)+"/files/"+escapeSitePath(sitePath), "")
}

func restHandoff(method, route, contentType string) map[string]any {
	handoff := map[string]any{
		"method":               method,
		"url":                  route,
		"authorization_header": "Authorization: Bearer $AIDROP_BEARER_TOKEN",
	}
	if contentType != "" {
		handoff["content_type"] = contentType
	}
	return handoff
}

func artifactBasePath(project, artifact string) string {
	return "/api/v1/projects/" + project + "/artifacts/" + artifact
}

func escapeSitePath(sitePath string) string {
	parts := strings.Split(sitePath, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func isTextualContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/javascript" || mediaType == "application/xml" || mediaType == "image/svg+xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}
