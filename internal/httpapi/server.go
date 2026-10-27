package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"aidrop/internal/config"
	"aidrop/internal/mdview"
	"aidrop/internal/storage"
	"aidrop/internal/zipsite"
)

const maxHTMLBody = 5 << 20

type server struct {
	bearerToken    string
	allowedOrigins []string
	repository     *storage.Repository
}

// NewHandler builds the public and management routes for the service.
func NewHandler(cfg config.Config, repository *storage.Repository) http.Handler {
	s := &server{
		bearerToken:    cfg.BearerToken,
		allowedOrigins: cfg.AllowedOrigins,
		repository:     repository,
	}
	management := http.NewServeMux()
	management.HandleFunc("/projects", s.projects)
	management.HandleFunc("/projects/{project}/artifacts", s.projectArtifacts)
	management.HandleFunc("/projects/{project}", s.project)
	management.HandleFunc("/projects/{project}/artifacts/{artifact}/source", s.source)
	management.HandleFunc("/projects/{project}/artifacts/{artifact}/files/{path...}", s.siteFile)
	management.HandleFunc("/projects/{project}/artifacts/{artifact}", s.manageArtifact)
	management.HandleFunc("/", s.managementNotFound)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("/api/v1", s.managementCORS(s.authenticate(http.HandlerFunc(s.managementNotFound))))
	mux.Handle("/api/v1/", s.managementCORS(s.authenticate(http.StripPrefix("/api/v1", management))))
	mux.Handle("/mcp", s.mcpCORS(s.authenticate(newMCPHTTPHandler(s))))
	mux.HandleFunc("GET /assets/mermaid.tiny.min.js", s.mermaidJavaScript)
	mux.HandleFunc("GET /assets/plantuml-frame.html", s.plantUMLRenderer)
	// Keep relative links inside the preview's internal path so ../ returns to site content.
	mux.HandleFunc("/_aidrop/content/{project}/{artifact}/", s.publicArtifactContent)
	mux.HandleFunc("/p/{project}/", s.publicProject)
	mux.HandleFunc("/p/{project}/{artifact}/", s.publicArtifact)
	mux.HandleFunc("/", s.home)
	return mux
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) mermaidJavaScript(w http.ResponseWriter, _ *http.Request) {
	script := mdview.MermaidJavaScript()
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(script)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(script)
}

func (s *server) plantUMLRenderer(w http.ResponseWriter, r *http.Request) {
	page := mdview.PlantUMLRendererPage()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", mdview.PlantUMLRendererCSP())
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(page)
}

func (s *server) notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "route not found")
}

func (s *server) managementNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "management route not found")
}

func (s *server) projects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	projects, err := s.repository.ListProjects()
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *server) projectArtifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	artifacts, err := s.repository.ListArtifacts(r.PathValue("project"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifacts)
}

func (s *server) project(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, "DELETE")
		return
	}
	project := r.PathValue("project")
	if err := storage.ValidateSlug(project); err != nil {
		writeStorageError(w, err)
		return
	}
	query := r.URL.Query()
	confirmations, exists := query["confirm"]
	if !exists || len(confirmations) == 0 || confirmations[0] == "" {
		writeStorageError(w, storage.ErrConfirmationRequired)
		return
	}
	if len(query) != 1 || len(confirmations) != 1 {
		writeStorageError(w, storage.ErrConfirmationMismatch)
		return
	}
	if err := s.repository.DeleteProject(project, confirmations[0]); err != nil {
		writeStorageError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasValidBearerToken(r.Header.Get("Authorization"), s.bearerToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) manageArtifact(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	artifact := r.PathValue("artifact")
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		if err := storage.ValidateSlug(project); err != nil {
			writeStorageError(w, err)
			return
		}
		if err := storage.ValidateSlug(artifact); err != nil {
			writeStorageError(w, err)
			return
		}
	}

	switch r.Method {
	case http.MethodPost:
		s.createArtifact(w, r, project, artifact)
	case http.MethodPut:
		s.replaceArtifact(w, r, project, artifact)
	case http.MethodGet:
		metadata, err := s.repository.GetMetadata(project, artifact)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, metadata)
	case http.MethodDelete:
		if err := s.repository.DeleteArtifact(project, artifact); err != nil {
			writeStorageError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, "GET, POST, PUT, DELETE")
	}
}

func (s *server) createArtifact(w http.ResponseWriter, r *http.Request, project, artifact string) {
	input, ok := readArtifactBody(w, r, project, artifact)
	if !ok {
		return
	}
	metadata, err := s.repository.Create(input)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, metadata)
}

func (s *server) replaceArtifact(w http.ResponseWriter, r *http.Request, project, artifact string) {
	input, ok := readArtifactBody(w, r, project, artifact)
	if !ok {
		return
	}
	metadata, err := s.repository.Replace(input)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, metadata)
}

func (s *server) source(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	metadata, source, err := s.repository.Get(r.PathValue("project"), r.PathValue("artifact"))
	if err != nil {
		writeStorageError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(metadata.Size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(source)
}

func (s *server) siteFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	sitePath := r.PathValue("path")
	contents, err := s.repository.GetSiteFile(r.PathValue("project"), r.PathValue("artifact"), sitePath)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	serveSiteFile(w, sitePath, contents)
}

func (s *server) publicArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	project := r.PathValue("project")
	artifact := r.PathValue("artifact")
	if err := storage.ValidateSlug(project); err != nil {
		writeStorageError(w, err)
		return
	}
	if err := storage.ValidateSlug(artifact); err != nil {
		writeStorageError(w, err)
		return
	}
	basePath := "/p/" + project + "/" + artifact + "/"
	if !strings.HasPrefix(r.URL.Path, basePath) {
		s.notFound(w, r)
		return
	}
	if r.URL.Path == basePath {
		metadata, body, err := s.repository.GetPublicRoot(project, artifact)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		if metadata.Format == storage.FormatMarkdown {
			page, hasPlantUML, err := mdview.RenderPage(project, artifact, body)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "render_failed", "could not render Markdown artifact")
				return
			}
			setPublicArtifactHeaders(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			contentSecurityPolicy := "default-src 'none'; style-src 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
			if hasPlantUML {
				contentSecurityPolicy += "; frame-src 'self'"
			}
			w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Length", strconv.Itoa(len(page)))
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write(page)
			}
			return
		}
		if metadata.Format != storage.FormatHTML && metadata.Format != storage.FormatZIP {
			s.notFound(w, r)
			return
		}
		if r.URL.Query().Get("_aidrop_content") != "1" {
			page, err := renderArtifactViewer(project, artifact, publicArtifactContentPath(project, artifact))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "render_failed", "could not render artifact viewer")
				return
			}
			setPublicArtifactHeaders(w)
			writeArtifactViewer(w, page, r.Method == http.MethodHead)
			return
		}
		entryPoint := metadata.EntryPoint
		if metadata.Format == storage.FormatHTML {
			entryPoint = "index.html"
		}
		servePublicSiteFile(w, entryPoint, body, r.Method == http.MethodHead)
		return
	}
	sitePath := strings.TrimPrefix(r.URL.Path, basePath)
	if strings.HasSuffix(sitePath, "/") {
		sitePath = strings.TrimSuffix(sitePath, "/") + "/index.html"
	}
	contents, err := s.repository.GetSiteFile(project, artifact, sitePath)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidSitePath) {
			s.notFound(w, r)
			return
		}
		writeStorageError(w, err)
		return
	}
	servePublicSiteFile(w, sitePath, contents, r.Method == http.MethodHead)
}

func (s *server) publicArtifactContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	project := r.PathValue("project")
	artifact := r.PathValue("artifact")
	if err := storage.ValidateSlug(project); err != nil {
		writeStorageError(w, err)
		return
	}
	if err := storage.ValidateSlug(artifact); err != nil {
		writeStorageError(w, err)
		return
	}
	basePath := publicArtifactContentPath(project, artifact)
	if !strings.HasPrefix(r.URL.Path, basePath) {
		s.notFound(w, r)
		return
	}
	metadata, err := s.repository.GetMetadata(project, artifact)
	if err != nil {
		writeStorageError(w, err)
		return
	}
	if metadata.Format != storage.FormatHTML && metadata.Format != storage.FormatZIP {
		s.notFound(w, r)
		return
	}
	sitePath := strings.TrimPrefix(r.URL.Path, basePath)
	if sitePath == "" {
		_, contents, err := s.repository.GetPublicRoot(project, artifact)
		if err != nil {
			writeStorageError(w, err)
			return
		}
		entryPoint := metadata.EntryPoint
		if metadata.Format == storage.FormatHTML {
			entryPoint = "index.html"
		}
		servePublicSiteFile(w, entryPoint, contents, r.Method == http.MethodHead)
		return
	}
	if metadata.Format != storage.FormatZIP {
		s.notFound(w, r)
		return
	}
	if strings.HasSuffix(sitePath, "/") {
		sitePath = strings.TrimSuffix(sitePath, "/") + "/index.html"
	}
	contents, err := s.repository.GetSiteFile(project, artifact, sitePath)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidSitePath) {
			s.notFound(w, r)
			return
		}
		writeStorageError(w, err)
		return
	}
	servePublicSiteFile(w, sitePath, contents, r.Method == http.MethodHead)
}

func publicArtifactContentPath(project, artifact string) string {
	return "/_aidrop/content/" + project + "/" + artifact + "/"
}

func readArtifactBody(w http.ResponseWriter, r *http.Request, project, artifact string) (storage.NewArtifact, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be text/html, text/markdown, or application/zip")
		return storage.NewArtifact{}, false
	}
	var format storage.Format
	var limit int64
	var label string
	switch mediaType {
	case string(storage.FormatHTML):
		format, limit, label = storage.FormatHTML, maxHTMLBody, "HTML"
	case string(storage.FormatMarkdown):
		format, limit, label = storage.FormatMarkdown, maxHTMLBody, "Markdown"
	case string(storage.FormatZIP):
		format, limit, label = storage.FormatZIP, zipsite.MaxCompressedSize, "ZIP"
	default:
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be text/html, text/markdown, or application/zip")
		return storage.NewArtifact{}, false
	}
	source, ok := readBoundedBody(w, r, limit, label)
	if !ok {
		return storage.NewArtifact{}, false
	}
	input := storage.NewArtifact{Project: project, Artifact: artifact, Format: format, Source: source}
	if format == storage.FormatMarkdown && !utf8.Valid(source) {
		writeError(w, http.StatusBadRequest, "invalid_markdown", "Markdown body must be valid UTF-8")
		return storage.NewArtifact{}, false
	}
	if format == storage.FormatZIP {
		site, err := zipsite.Extract(source)
		if errors.Is(err, zipsite.ErrLimitExceeded) {
			writeError(w, http.StatusRequestEntityTooLarge, "archive_limit_exceeded", "ZIP archive exceeds extraction safety limits")
			return storage.NewArtifact{}, false
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_archive", "ZIP archive is invalid or does not contain a safe website")
			return storage.NewArtifact{}, false
		}
		input.EntryPoint = site.EntryPoint
		input.SiteFiles = site.Files
	}
	return input, true
}

func readBoundedBody(w http.ResponseWriter, r *http.Request, limit int64, label string) ([]byte, bool) {
	if r.ContentLength > limit {
		writePayloadTooLarge(w, label)
		return nil, false
	}
	source, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writePayloadTooLarge(w, label)
		} else {
			writeError(w, http.StatusBadRequest, "invalid_body", "could not read request body")
		}
		return nil, false
	}
	if len(source) == 0 {
		writeError(w, http.StatusBadRequest, "empty_payload", "artifact body must not be empty")
		return nil, false
	}
	return source, true
}

func writePayloadTooLarge(w http.ResponseWriter, label string) {
	limit := "5 MiB"
	if label == "ZIP" {
		limit = "25 MiB"
	}
	writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", label+" body exceeds "+limit)
}

func serveSiteFile(w http.ResponseWriter, sitePath string, contents []byte) {
	serveSiteFileHeadAware(w, sitePath, contents, false)
}

func servePublicSiteFile(w http.ResponseWriter, sitePath string, contents []byte, head bool) {
	setPublicArtifactHeaders(w)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	serveSiteFileHeadAware(w, sitePath, contents, head)
}

func serveSiteFileHeadAware(w http.ResponseWriter, sitePath string, contents []byte, head bool) {
	contentType := contentTypeForSitePath(sitePath)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if isHTMLPath(sitePath) {
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(contents)))
	w.WriteHeader(http.StatusOK)
	if !head {
		_, _ = w.Write(contents)
	}
}

func setPublicArtifactHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func contentTypeForSitePath(sitePath string) string {
	extension := strings.ToLower(filepath.Ext(sitePath))
	switch extension {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".wasm":
		return "application/wasm"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	}
	if contentType := mime.TypeByExtension(extension); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

func isHTMLPath(sitePath string) bool {
	extension := strings.ToLower(filepath.Ext(sitePath))
	return extension == ".html" || extension == ".htm"
}

func writeStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrInvalidSlug):
		writeError(w, http.StatusBadRequest, "invalid_slug", "project and artifact slugs must be lowercase kebab-case")
	case errors.Is(err, storage.ErrInvalidFormat):
		writeError(w, http.StatusBadRequest, "invalid_format", "artifact format is not supported")
	case errors.Is(err, storage.ErrInvalidSitePath):
		writeError(w, http.StatusBadRequest, "invalid_site_path", "site file path is invalid")
	case errors.Is(err, storage.ErrConfirmationRequired):
		writeError(w, http.StatusBadRequest, "confirmation_required", "confirm must exactly match the project slug")
	case errors.Is(err, storage.ErrConfirmationMismatch):
		writeError(w, http.StatusBadRequest, "confirmation_mismatch", "confirm must exactly match the project slug")
	case errors.Is(err, storage.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "artifact already exists")
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "artifact not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func hasValidBearerToken(header, expected string) bool {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	providedHash := sha256.Sum256([]byte(parts[1]))
	expectedHash := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
