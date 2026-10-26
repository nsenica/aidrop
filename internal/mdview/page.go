package mdview

import (
	"bytes"
	"encoding/base64"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

type pageData struct {
	Project      string
	Artifact     string
	RenderedHTML template.HTML
	SourceBase64 string
	HasMermaid   bool
}

var pageTemplate = template.Must(template.New("markdown-page").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Project}} / {{.Artifact}}</title>
  <style>
    :root { color-scheme: light; font: 16px/1.65 Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color: #172235; background: #f4f6fb; }
    * { box-sizing: border-box; }
    html, body { width: 100%; min-height: 100%; margin: 0; }
    body { min-height: 100vh; display: flex; flex-direction: column; background: radial-gradient(ellipse at 8% 0, #e8eeff 0, transparent 32rem), #f4f6fb; }
    a { color: #315ac1; overflow-wrap: anywhere; }
    a:focus-visible, button:focus-visible, summary:focus-visible { outline: 3px solid #8ba7ff; outline-offset: 3px; border-radius: 5px; }
    .page-shell { width: min(100%, 920px); flex: 1; margin: 0 auto; padding: 30px 26px 64px; }
    .site-header { z-index: 1; display: flex; min-height: 68px; align-items: center; justify-content: space-between; gap: 14px 24px; padding: 12px 24px; border-bottom: 1px solid #dfe5ef; background: #fff; box-shadow: 0 3px 14px #263c630c; }
    .brand { display: inline-flex; align-items: center; gap: 9px; color: #1b2940; font-size: .95rem; font-weight: 760; letter-spacing: -.025em; text-decoration: none; }
    .brand-mark { display: grid; width: 32px; height: 32px; place-items: center; border-radius: 10px; background: linear-gradient(145deg, #3769e8, #2349b4); box-shadow: 0 4px 11px #345bc533; color: #fff; font-size: .84rem; font-weight: 800; }
    .preview-label { flex: none; padding: 6px 9px; border: 1px solid #e1e8f5; border-radius: 999px; background: #f7f9fd; color: #6d7b92; font-size: .7rem; font-weight: 700; letter-spacing: .04em; text-transform: uppercase; }
    .document-card { overflow: hidden; border: 1px solid #e1e7f0; border-radius: 20px; background: #fff; box-shadow: 0 12px 38px #263c6312; }
    .document-header { padding: 34px 42px 29px; border-bottom: 1px solid #edf0f5; background: linear-gradient(135deg, #fbfcff, #fff 68%); }
    .breadcrumbs { display: flex; min-width: 0; flex: 1; align-items: center; justify-content: center; flex-wrap: wrap; gap: 9px; margin: 0; color: #8993a5; font-size: .83rem; }
    .breadcrumbs a { color: #53627b; text-decoration: none; }
    .breadcrumbs a:hover { color: #315ac1; }
    .breadcrumbs [aria-current="page"] { max-width: min(28vw, 280px); overflow: hidden; color: #27364d; font-weight: 700; text-overflow: ellipsis; white-space: nowrap; }
    .document-kind { display: inline-flex; align-items: center; gap: 7px; margin: 0 0 12px; padding: 5px 9px; border: 1px solid #dfe7fb; border-radius: 999px; background: #f2f5ff; color: #4563b3; font-size: .7rem; font-weight: 750; letter-spacing: .06em; text-transform: uppercase; }
    .document-kind::before { width: 6px; height: 6px; border-radius: 50%; background: #5978d3; content: ""; }
    .document-header h1 { margin: 0; color: #18243a; font-size: clamp(2rem, 5vw, 3rem); font-weight: 730; letter-spacing: -.055em; line-height: 1.1; overflow-wrap: anywhere; }
    .document-header p:last-child { margin: 10px 0 0; color: #758198; font-size: .93rem; }
    .prose { max-width: 78ch; margin: 0 auto; padding: 35px 42px 45px; color: #2e3b50; overflow-wrap: anywhere; }
    .prose h1, .prose h2, .prose h3, .prose h4, .prose h5, .prose h6 { color: #1c2940; font-weight: 700; letter-spacing: -.035em; line-height: 1.28; }
    .prose h1 { margin: 1.8em 0 .65em; font-size: 2rem; }
    .prose h2 { margin: 1.65em 0 .55em; font-size: 1.55rem; }
    .prose h3 { margin: 1.45em 0 .5em; font-size: 1.25rem; }
    .prose h1:first-child, .prose h2:first-child, .prose h3:first-child { margin-top: 0; }
    .prose p, .prose ul, .prose ol { margin: 0 0 1.05em; }
    .prose li + li { margin-top: .35em; }
    .prose img { max-width: 100%; border-radius: 12px; }
    .prose table { display: block; max-width: 100%; border-collapse: collapse; overflow: auto; font-size: .9rem; }
    .prose th, .prose td { padding: .65rem .8rem; border: 1px solid #e1e7f0; text-align: left; }
    .prose th { background: #f5f7fb; color: #33435c; font-weight: 700; }
    .prose tr:nth-child(even) td { background: #fafbfe; }
    .prose blockquote { margin: 1.4rem 0; padding: .2rem 0 .2rem 1rem; border-left: 3px solid #6a86d6; color: #63718a; }
    .prose blockquote > :last-child { margin-bottom: 0; }
    .prose pre { margin: 1.35rem 0; padding: 1.1rem 1.2rem; border: 1px solid #2d3c58; border-radius: 12px; background: #18243a; color: #e7edfa; overflow: auto; }
    pre code { background: none; padding: 0; }
    .prose pre code { color: inherit; }
    .prose .mermaid-diagram { max-width: 100%; margin: 1.35rem 0; overflow-x: auto; text-align: center; }
    .prose .mermaid-diagram svg { max-width: 100%; height: auto; }
    .prose .mermaid-fallback { margin: 1.1rem 0 -.9rem; color: #8a5a12; font-size: .88rem; }
    .prose code { font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: .88em; }
    .prose :not(pre) > code { padding: .15rem .35rem; border: 1px solid #e5eaf2; border-radius: 5px; background: #f4f6fa; color: #425477; }
    .prose hr { height: 1px; margin: 2rem 0; border: 0; background: #e6eaf1; }
    .source-panel { margin: 0 42px 36px; padding-top: 18px; border-top: 1px solid #e9edf4; }
    .source-panel summary { display: flex; align-items: center; justify-content: space-between; gap: 1rem; color: #35455f; cursor: pointer; font-size: .88rem; font-weight: 700; list-style-position: inside; }
    .source-panel summary::marker { color: #5874c8; }
    .source-panel[open] summary { margin-bottom: 12px; }
    .source-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 1rem; color: #7a879b; font-size: .78rem; }
    button { padding: 8px 12px; border: 1px solid #dce5fa; border-radius: 9px; background: #f6f8ff; color: #3659b4; cursor: pointer; font: inherit; font-size: .78rem; font-weight: 700; line-height: 1.35; }
    button:hover { border-color: #b7c7f3; background: #edf2ff; }
    textarea { display: block; width: 100%; min-height: 18rem; margin-top: 12px; padding: 14px; resize: vertical; border: 1px solid #dfe5ef; border-radius: 11px; background: #f8f9fc; color: #2c3a50; font: .84rem/1.6 ui-monospace, SFMono-Regular, Consolas, monospace; }
    .site-footer { margin-top: 24px; color: #8993a5; font-size: .77rem; text-align: center; }
    @media (max-width: 640px) {
      .page-shell { padding: 21px 15px 38px; }
      .site-header { min-height: 0; flex-wrap: wrap; padding: 11px 14px; }
      .breadcrumbs { order: 3; width: 100%; justify-content: flex-start; }
      .breadcrumbs [aria-current="page"] { max-width: 48vw; }
      .preview-label { margin-left: auto; }
      .document-header { padding: 24px 22px 21px; }
      .prose { padding: 26px 22px 32px; }
      .source-panel { margin: 0 22px 25px; }
      .prose pre { margin-right: -8px; margin-left: -8px; }
    }
    @media (prefers-reduced-motion: reduce) { *, *::before, *::after { scroll-behavior: auto !important; transition-duration: .01ms !important; } }
  </style>
</head>
<body>
  <header class="site-header">
    <a class="brand" href="/"><span class="brand-mark" aria-hidden="true">A</span>AI Drop</a>
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <a href="/">Home</a><span aria-hidden="true">/</span>
      <a href="/p/{{.Project}}/">{{.Project}}</a><span aria-hidden="true">/</span>
      <span aria-current="page">{{.Artifact}}</span>
    </nav>
    <span class="preview-label">Markdown preview</span>
  </header>
  <div class="page-shell">
    <main class="document-card">
      <header class="document-header">
        <p class="document-kind">Markdown document</p>
        <h1>{{.Artifact}}</h1>
        <p>Published in <a href="/p/{{.Project}}/">{{.Project}}</a></p>
      </header>
      <article class="prose" aria-label="Rendered Markdown">{{.RenderedHTML}}</article>
      <details class="source-panel">
        <summary>View original Markdown source</summary>
        <div class="source-toolbar"><span>The exact uploaded source</span><button id="copy-source" type="button">Copy source</button></div>
        <textarea id="markdown-source" aria-label="Original Markdown source" readonly spellcheck="false"></textarea>
        <input id="markdown-source-data" type="hidden" value="{{.SourceBase64}}">
      </details>
    </main>
    <footer class="site-footer">AI Drop · Readable project documents</footer>
  </div>
  <script>
    (() => {
      const encoded = document.getElementById("markdown-source-data").value;
      const bytes = Uint8Array.from(atob(encoded), character => character.charCodeAt(0));
      const source = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
      const textarea = document.getElementById("markdown-source");
      textarea.value = source;
      document.getElementById("copy-source").addEventListener("click", async () => {
        textarea.focus();
        textarea.select();
        if (navigator.clipboard && window.isSecureContext) {
          try {
            await navigator.clipboard.writeText(source);
            return;
          } catch (_) {}
        }
        document.execCommand("copy");
      });
    })();
  </script>
  {{if .HasMermaid}}
  <script src="/assets/mermaid.tiny.min.js" defer></script>
  <script>
    document.addEventListener("DOMContentLoaded", async () => {
      const showFallback = pre => {
        const notice = document.createElement("p");
        notice.className = "mermaid-fallback";
        notice.textContent = "Diagram preview unavailable; Mermaid source is shown below.";
        pre.parentNode.insertBefore(notice, pre);
      };
      const blocks = document.querySelectorAll(".prose pre > code.language-mermaid");
      if (!window.mermaid) {
        blocks.forEach(code => showFallback(code.parentElement));
        return;
      }
      window.mermaid.initialize({
        startOnLoad: false,
        securityLevel: "strict",
        secure: ["secure", "securityLevel", "startOnLoad", "maxTextSize", "maxEdges", "suppressErrorRendering"],
        theme: "neutral",
        maxTextSize: 50000,
        maxEdges: 500,
        suppressErrorRendering: true
      });
      for (const [index, code] of blocks.entries()) {
        const pre = code.parentElement;
        try {
          const source = code.textContent;
          const parsed = await window.mermaid.parse(source, { suppressErrors: true });
          if (!parsed) {
            showFallback(pre);
            continue;
          }
          const rendered = await window.mermaid.render("aidrop-mermaid-" + index, source);
          const diagram = document.createElement("div");
          diagram.className = "mermaid-diagram";
          diagram.setAttribute("role", "img");
          diagram.setAttribute("aria-label", "Mermaid diagram");
          diagram.innerHTML = rendered.svg;
          pre.replaceWith(diagram);
        } catch (_) {
          showFallback(pre);
        }
      }
    }, { once: true });
  </script>
  {{end}}
</body>
</html>
`))

// RenderPage turns Markdown into a service-owned page. Goldmark's safe HTML
// renderer omits raw HTML and potentially dangerous link destinations by default.
// The only trusted HTML passed to the template is output from that renderer.
func RenderPage(project, artifact string, source []byte) ([]byte, error) {
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM))
	var rendered bytes.Buffer
	if err := parser.Convert(source, &rendered); err != nil {
		return nil, err
	}
	data := pageData{
		Project:      project,
		Artifact:     artifact,
		RenderedHTML: template.HTML(rendered.String()),
		SourceBase64: base64.StdEncoding.EncodeToString(source),
		HasMermaid:   bytes.Contains(rendered.Bytes(), []byte(`<code class="language-mermaid">`)),
	}
	var page bytes.Buffer
	if err := pageTemplate.Execute(&page, data); err != nil {
		return nil, err
	}
	return page.Bytes(), nil
}
