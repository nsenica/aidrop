package httpapi

import (
	"bytes"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"aidrop/internal/storage"
)

type projectIndexData struct {
	Projects []string
}

type artifactListing struct {
	Artifact       string
	Format         storage.Format
	PublicURL      string
	ModifiedAtISO  string
	ModifiedAtText string
}

type projectPageData struct {
	Project   string
	Found     bool
	Artifacts []artifactListing
}

var catalogTemplate = template.Must(template.New("catalog").Parse(`{{define "styles"}}
<style>
  :root { color-scheme: light; font: 16px/1.55 Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color: #172235; background: #f4f6fb; }
  * { box-sizing: border-box; }
  body { min-height: 100vh; margin: 0; background: radial-gradient(ellipse at 8% 0, #e8eeff 0, transparent 32rem), #f4f6fb; }
  a { color: inherit; }
  a:focus-visible { outline: 3px solid #8ba7ff; outline-offset: 4px; border-radius: 5px; }
  .page-shell { width: min(100%, 1120px); margin: 0 auto; padding: 30px 28px 56px; }
  .site-header { display: flex; align-items: center; justify-content: space-between; gap: 1rem; margin-bottom: 48px; padding-bottom: 20px; border-bottom: 1px solid #dfe5ef; }
  .brand { display: inline-flex; align-items: center; gap: 10px; color: #1b2940; font-size: .98rem; font-weight: 760; letter-spacing: -.025em; text-decoration: none; }
  .brand a { color: inherit; text-decoration: none; }
  .brand a:hover { color: #315ed0; }
  .brand-mark { display: grid; width: 34px; height: 34px; place-items: center; border-radius: 11px; background: linear-gradient(145deg, #3769e8, #2349b4); box-shadow: 0 5px 12px #345bc533; color: #fff; font-size: .88rem; font-weight: 800; }
  .header-label { color: #758198; font-size: .82rem; }
  .breadcrumbs { display: flex; align-items: center; flex-wrap: wrap; gap: 9px; margin: 0 0 15px; color: #8791a3; font-size: .86rem; }
  .breadcrumbs a { color: #51617b; text-decoration: none; }
  .breadcrumbs a:hover { color: #315ed0; }
  .page-heading { display: flex; align-items: flex-end; justify-content: space-between; gap: 1.5rem; margin: 0 0 30px; }
  .eyebrow { margin: 0 0 8px; color: #4568c5; font-size: .71rem; font-weight: 760; letter-spacing: .105em; text-transform: uppercase; }
  h1 { margin: 0; color: #18243a; font-size: clamp(2.15rem, 5vw, 3.15rem); font-weight: 720; letter-spacing: -.055em; line-height: 1.08; overflow-wrap: anywhere; }
  .lede { max-width: 58ch; margin: 12px 0 0; color: #68768d; font-size: 1rem; }
  .count-pill, .status-pill { flex: none; padding: 8px 12px; border: 1px solid #dce5ff; border-radius: 999px; background: #edf2ff; color: #3659b4; font-size: .78rem; font-weight: 700; white-space: nowrap; }
  .status-pill { border-color: #f1dfd6; background: #fff4ee; color: #a34d2c; }
  .project-grid, .artifact-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(min(100%, 300px), 1fr)); gap: 15px; margin: 0; padding: 0; list-style: none; }
  .project-card, .artifact-card { min-width: 0; border: 1px solid #e1e7f0; border-radius: 16px; background: #fff; box-shadow: 0 5px 20px #263c6310; }
  .project-card { display: flex; min-height: 156px; align-items: flex-start; gap: 14px; padding: 21px; text-decoration: none; transition: transform .16s ease, border-color .16s ease, box-shadow .16s ease; }
  .project-card:hover { transform: translateY(-2px); border-color: #c3d0f5; box-shadow: 0 12px 26px #263c6318; }
  .project-icon { display: grid; width: 39px; height: 39px; flex: none; place-items: center; border: 1px solid #e3eaff; border-radius: 12px; background: #f2f5ff; color: #4165c2; font-size: 1.2rem; }
  .project-card__body { display: grid; min-width: 0; gap: 4px; }
  .card-kicker { color: #8994a8; font-size: .72rem; font-weight: 700; letter-spacing: .065em; text-transform: uppercase; }
  .project-card__title { color: #1c2b43; font-size: 1.08rem; font-weight: 720; overflow-wrap: anywhere; }
  .project-card__description { color: #778399; font-size: .84rem; }
  .card-arrow { margin: 2px 0 0 auto; color: #8b97ad; font-size: 1.05rem; }
  .artifact-list { grid-template-columns: 1fr; gap: 12px; }
  .artifact-card { display: flex; align-items: center; justify-content: space-between; gap: 1.5rem; padding: 19px 21px; transition: border-color .16s ease, box-shadow .16s ease; }
  .artifact-card:hover { border-color: #ccd7ef; box-shadow: 0 9px 24px #263c6315; }
  .artifact-main { min-width: 0; }
  .artifact-title { margin: 0 0 10px; font-size: 1.08rem; font-weight: 720; letter-spacing: -.02em; overflow-wrap: anywhere; }
  .artifact-title a { text-decoration: none; }
  .artifact-title a:hover { color: #315ed0; }
  .artifact-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 9px 15px; color: #758198; font-size: .79rem; }
  .format { display: inline-flex; align-items: center; gap: 7px; padding: 4px 9px; border: 1px solid #e3e9f2; border-radius: 999px; background: #f7f9fc; color: #56657d; font: 650 .74rem/1.35 ui-monospace, SFMono-Regular, Consolas, monospace; }
  .format-dot { width: 6px; height: 6px; border-radius: 50%; background: #637fce; }
  .modified { white-space: nowrap; }
  .artifact-open { display: inline-flex; flex: none; align-items: center; gap: 8px; padding: 9px 12px; border: 1px solid #dce5fa; border-radius: 10px; background: #f6f8ff; color: #3659b4; font-size: .79rem; font-weight: 700; text-decoration: none; transition: background .15s ease, border-color .15s ease; }
  .artifact-open:hover { border-color: #b7c7f3; background: #edf2ff; }
  .empty-state { display: flex; align-items: flex-start; gap: 17px; max-width: 760px; padding: 25px; border: 1px dashed #cad4e4; border-radius: 17px; background: #ffffffbd; }
  .empty-icon { display: grid; width: 42px; height: 42px; flex: none; place-items: center; border-radius: 14px; background: #edf2ff; color: #4868ba; font-size: 1.15rem; font-weight: 750; }
  .empty-state h2 { margin: 0; color: #25344c; font-size: 1.05rem; letter-spacing: -.015em; }
  .empty-state p:last-child { margin: 5px 0 0; color: #758198; font-size: .9rem; }
  .site-footer { margin-top: 44px; padding-top: 16px; border-top: 1px solid #dfe5ef; color: #8993a5; font-size: .77rem; }
  @media (max-width: 620px) {
    .page-shell { padding: 22px 17px 40px; }
    .site-header { margin-bottom: 34px; padding-bottom: 16px; }
    .page-heading { align-items: flex-start; flex-direction: column; gap: 14px; margin-bottom: 22px; }
    .artifact-card { align-items: flex-start; flex-direction: column; gap: 14px; padding: 17px; }
    .artifact-open { align-self: stretch; justify-content: center; }
    .empty-state { padding: 19px; }
  }
  @media (prefers-reduced-motion: reduce) {
    *, *::before, *::after { scroll-behavior: auto !important; transition-duration: .01ms !important; }
  }
</style>
{{end}}

{{define "index"}}
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>AI Drop projects</title>
  {{template "styles" .}}
</head>
<body>
  <div class="page-shell">
    <header class="site-header">
      <div class="brand"><span class="brand-mark" aria-hidden="true">A</span><a href="/">AI Drop</a></div>
      <span class="header-label">Public workspace</span>
    </header>
    <main>
      <nav class="breadcrumbs" aria-label="Breadcrumb"><span>Workspace</span><span aria-hidden="true">/</span><span aria-current="page">Projects</span></nav>
      <div class="page-heading">
        <div><p class="eyebrow">Published work</p><h1>Projects</h1><p class="lede">Browse the work published across your AI Drop workspace.</p></div>
        {{if .Projects}}<span class="count-pill">{{len .Projects}} projects</span>{{end}}
      </div>
    {{if .Projects}}
    <ul class="project-grid">
      {{range .Projects}}<li><a class="project-card" href="/p/{{.}}/"><span class="project-icon" aria-hidden="true">◇</span><span class="project-card__body"><span class="card-kicker">Project</span><span class="project-card__title">{{.}}</span><span class="project-card__description">Browse published artifacts</span></span><span class="card-arrow" aria-hidden="true">↗</span></a></li>{{end}}
    </ul>
    {{else}}
    <section class="empty-state" aria-label="No published projects"><span class="empty-icon" aria-hidden="true">◇</span><div><p class="eyebrow">Nothing published yet</p><h2>No projects yet</h2><p>When a project publishes its first artifact, it will appear here.</p></div></section>
    {{end}}
    </main>
    <footer class="site-footer">AI Drop · A clear home for published work</footer>
  </div>
</body>
</html>
{{end}}

{{define "project"}}
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{if .Found}}{{.Project}}{{else}}Project not found{{end}} · AI Drop</title>
  {{template "styles" .}}
</head>
<body>
  <div class="page-shell">
    <header class="site-header">
      <div class="brand"><span class="brand-mark" aria-hidden="true">A</span><a href="/">AI Drop</a></div>
      <span class="header-label">Public workspace</span>
    </header>
    <main>
      <nav class="breadcrumbs" aria-label="Breadcrumb"><a href="/">Projects</a><span aria-hidden="true">/</span><span aria-current="page">{{.Project}}</span></nav>
      <div class="page-heading">
        <div><p class="eyebrow">{{if .Found}}Project workspace{{else}}Unavailable{{end}}</p><h1>{{.Project}}</h1><p class="lede">{{if .Found}}Published artifacts for this project, together in one place.{{else}}This project isn’t available at this address.{{end}}</p></div>
        {{if .Found}}<span class="count-pill">{{len .Artifacts}} artifacts</span>{{else}}<span class="status-pill">404 · Not found</span>{{end}}
      </div>
    {{if not .Found}}
    <section class="empty-state" aria-label="Project not found"><span class="empty-icon" aria-hidden="true">!</span><div><p class="eyebrow">Page unavailable</p><h2>This project was not found</h2><p>Check the project link or return to the project index.</p></div></section>
    {{else if .Artifacts}}
    <ul class="artifact-list">
      {{range .Artifacts}}
      <li class="artifact-card">
        <div class="artifact-main"><h2 class="artifact-title"><a href="{{.PublicURL}}">{{.Artifact}}</a></h2><div class="artifact-meta"><span class="format"><span class="format-dot" aria-hidden="true"></span>{{.Format}}</span><span class="modified">Modified <time datetime="{{.ModifiedAtISO}}">{{.ModifiedAtText}}</time></span></div></div>
        <a class="artifact-open" href="{{.PublicURL}}">Open artifact <span aria-hidden="true">↗</span></a>
      </li>
      {{end}}
    </ul>
    {{else}}
    <section class="empty-state" aria-label="No project artifacts"><span class="empty-icon" aria-hidden="true">◇</span><div><p class="eyebrow">A fresh start</p><h2>No artifacts yet</h2><p>Artifacts published to this project will be listed here.</p></div></section>
    {{end}}
    </main>
    <footer class="site-footer">AI Drop · Public project index</footer>
  </div>
</body>
</html>
{{end}}`))

func renderProjectIndex(projects []string) ([]byte, error) {
	var output bytes.Buffer
	if err := catalogTemplate.ExecuteTemplate(&output, "index", projectIndexData{Projects: projects}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func renderProjectPage(project string, artifacts []storage.Metadata, found bool) ([]byte, error) {
	pageArtifacts := make([]artifactListing, 0, len(artifacts))
	for _, artifact := range artifacts {
		modified := artifact.ModifiedAt.UTC()
		pageArtifacts = append(pageArtifacts, artifactListing{
			Artifact:       artifact.Artifact,
			Format:         artifact.Format,
			PublicURL:      artifact.PublicURL,
			ModifiedAtISO:  modified.Format(time.RFC3339Nano),
			ModifiedAtText: modified.Format("2006-01-02 15:04:05 UTC"),
		})
	}
	var output bytes.Buffer
	if err := catalogTemplate.ExecuteTemplate(&output, "project", projectPageData{Project: project, Found: found, Artifacts: pageArtifacts}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	if r.URL.Path != "/" {
		s.notFound(w, r)
		return
	}
	projects, err := s.repository.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	page, err := renderProjectIndex(projects)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeCatalogPage(w, http.StatusOK, page)
}

func (s *server) publicProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	project := r.PathValue("project")
	if r.URL.Path != "/p/"+project+"/" || storage.ValidateSlug(project) != nil {
		s.notFound(w, r)
		return
	}
	artifacts, err := s.repository.ListArtifacts(project)
	if errors.Is(err, storage.ErrNotFound) {
		page, renderErr := renderProjectPage(project, nil, false)
		if renderErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		writeCatalogPage(w, http.StatusNotFound, page)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	page, err := renderProjectPage(project, artifacts, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeCatalogPage(w, http.StatusOK, page)
}

func writeCatalogPage(w http.ResponseWriter, status int, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(status)
	_, _ = w.Write(page)
}
