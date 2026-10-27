package httpapi

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
)

type artifactViewerData struct {
	Project    string
	Artifact   string
	ContentURL string
}

var artifactViewerTemplate = template.Must(template.New("artifact-viewer").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Artifact}} · {{.Project}} · AI Drop</title>
  <style>
    :root { color-scheme: light; font: 14px/1.45 Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color: #172235; background: #f4f6fb; }
    * { box-sizing: border-box; }
    html, body { width: 100%; height: 100%; margin: 0; }
    body { display: flex; flex-direction: column; background: #f4f6fb; }
    a { color: inherit; }
    a:focus-visible { outline: 3px solid #8ba7ff; outline-offset: 3px; border-radius: 5px; }
    .toolbar { z-index: 1; display: flex; min-height: 68px; align-items: center; justify-content: space-between; gap: 14px 24px; padding: 12px 24px; border-bottom: 1px solid #dfe5ef; background: #fff; box-shadow: 0 3px 14px #263c630c; }
    .brand { display: inline-flex; flex: none; align-items: center; gap: 9px; color: #1b2940; font-size: .95rem; font-weight: 760; letter-spacing: -.025em; text-decoration: none; }
    .brand-mark { display: grid; width: 32px; height: 32px; place-items: center; border-radius: 10px; background: linear-gradient(145deg, #3769e8, #2349b4); box-shadow: 0 4px 11px #345bc533; color: #fff; font-size: .84rem; font-weight: 800; }
    .breadcrumbs { display: flex; min-width: 0; align-items: center; justify-content: center; flex-wrap: wrap; gap: 9px; color: #8993a5; font-size: .83rem; }
    .breadcrumbs a { color: #53627b; text-decoration: none; }
    .breadcrumbs a:hover { color: #315ac1; }
    .breadcrumbs [aria-current="page"] { max-width: min(28vw, 280px); overflow: hidden; color: #27364d; font-weight: 700; text-overflow: ellipsis; white-space: nowrap; }
    .preview-label { flex: none; padding: 6px 9px; border: 1px solid #e1e8f5; border-radius: 999px; background: #f7f9fd; color: #6d7b92; font-size: .7rem; font-weight: 700; letter-spacing: .04em; text-transform: uppercase; }
    main { display: flex; min-height: 0; flex: 1; padding: 10px; }
    iframe { display: block; width: 100%; min-height: 0; flex: 1; border: 1px solid #dce3ee; border-radius: 12px; background: #fff; box-shadow: 0 5px 20px #263c6310; }
    @media (max-width: 680px) {
      .toolbar { min-height: 0; flex-wrap: wrap; padding: 11px 14px; }
      .breadcrumbs { order: 3; width: 100%; justify-content: flex-start; }
      .breadcrumbs [aria-current="page"] { max-width: 48vw; }
      .preview-label { margin-left: auto; }
      main { padding: 7px; }
      iframe { border-radius: 9px; }
    }
    @media (prefers-reduced-motion: reduce) { *, *::before, *::after { scroll-behavior: auto !important; transition-duration: .01ms !important; } }
  </style>
</head>
<body>
  <header class="toolbar">
    <a class="brand" href="/"><span class="brand-mark" aria-hidden="true">A</span>AI Drop</a>
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <a href="/">Home</a><span aria-hidden="true">/</span>
      <a href="/p/{{.Project}}/">{{.Project}}</a><span aria-hidden="true">/</span>
      <span aria-current="page">{{.Artifact}}</span>
    </nav>
  <span class="preview-label">Published preview</span>
  </header>
  <main>
    <iframe src="{{.ContentURL}}" title="{{.Artifact}} published content" sandbox="allow-scripts"></iframe>
  </main>
</body>
</html>
`))

func renderArtifactViewer(project, artifact, contentURL string) ([]byte, error) {
	var page bytes.Buffer
	if err := artifactViewerTemplate.Execute(&page, artifactViewerData{
		Project:    project,
		Artifact:   artifact,
		ContentURL: contentURL,
	}); err != nil {
		return nil, err
	}
	return page.Bytes(), nil
}

func writeArtifactViewer(w http.ResponseWriter, page []byte, head bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(page)))
	w.WriteHeader(http.StatusOK)
	if !head {
		_, _ = w.Write(page)
	}
}
