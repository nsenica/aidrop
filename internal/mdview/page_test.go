package mdview

import (
	"encoding/base64"
	"html"
	"strings"
	"testing"
)

func TestRenderPageSupportsGFMAndSelectableOriginalSource(t *testing.T) {
	source := []byte("# Release report\r\n\r\n| Name | Status |\r\n| --- | --- |\r\n| API | ready |\r\n\r\n[API docs](https://example.test/docs)\r\n\r\n- one\r\n- two\r\n\r\n```go\r\nfmt.Println(\"ok\")\r\n```\r\n")
	page, err := RenderPage("demo", "report", source)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	contents := string(page)
	for _, want := range []string{
		"<h1>Release report</h1>",
		"<table>",
		"<a href=\"https://example.test/docs\">API docs</a>",
		"<ul>",
		"<pre><code class=\"language-go\">",
		"pre code { background: none; padding: 0; }",
		"View original Markdown source",
		"id=\"markdown-source\" aria-label=\"Original Markdown source\" readonly",
		"id=\"copy-source\" type=\"button\">Copy source</button>",
		"class=\"site-header\"",
		"aria-label=\"Breadcrumb\"",
		"href=\"/\">Home</a>",
		"href=\"/p/demo/\">demo</a>",
		"aria-current=\"page\">report</span>",
		"class=\"preview-label\">Markdown preview</span>",
		"class=\"prose\" aria-label=\"Rendered Markdown\"",
		"class=\"source-panel\"",
		"@media (max-width: 640px)",
		"textarea.select()",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
	if strings.Contains(contents, "mermaid.tiny.min.js") || strings.Contains(contents, "window.mermaid") {
		t.Error("ordinary Markdown page loaded the Mermaid renderer")
	}
	encoded := base64.StdEncoding.EncodeToString(source)
	if !strings.Contains(contents, `id="markdown-source-data" type="hidden" value="`+encoded+`"`) {
		t.Error("page source payload does not preserve the exact original bytes as Base64")
	}
}

func TestRenderPageSupportsMermaidAndPreservesFallbackSource(t *testing.T) {
	source := []byte("# System overview\n\n```mermaid\nflowchart TD\n  A[Client] --> B[API]\n```\n\n```mermaid\nsequenceDiagram\n  Client->>API: request\n```\n\n```mermaid\nclassDiagram\n  class Artifact\n```\n\n```mermaid\nnot a valid diagram\n```\n\nThe rest of the document remains readable.\n")
	page, err := RenderPage("demo", "overview", source)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	contents := string(page)
	for _, want := range []string{
		`<code class="language-mermaid">`,
		`src="/assets/mermaid.tiny.min.js"`,
		`securityLevel: "strict"`,
		`secure: ["secure", "securityLevel", "startOnLoad", "maxTextSize", "maxEdges", "suppressErrorRendering"]`,
		`maxTextSize: 50000`,
		`maxEdges: 500`,
		`suppressErrorRendering: true`,
		`window.mermaid.parse(source, { suppressErrors: true })`,
		"if (!parsed) {\n            showFallback(pre);",
		"catch (_) {\n          showFallback(pre);",
		"Diagram preview unavailable; Mermaid source is shown below.",
		"not a valid diagram",
		"The rest of the document remains readable.",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("rendered Mermaid page does not contain %q", want)
		}
	}
	parseCall := strings.Index(contents, `window.mermaid.parse(source, { suppressErrors: true })`)
	renderCall := strings.Index(contents, `window.mermaid.render("aidrop-mermaid-" + index, source)`)
	if parseCall < 0 || renderCall < 0 || parseCall > renderCall {
		t.Error("Mermaid syntax validation must run before rendering")
	}
	if strings.Contains(contents, "cdn.jsdelivr.net") || strings.Contains(contents, "unpkg.com") {
		t.Error("Mermaid page refers to an external asset host")
	}
	encoded := base64.StdEncoding.EncodeToString(source)
	if !strings.Contains(html.UnescapeString(contents), `id="markdown-source-data" type="hidden" value="`+encoded+`"`) {
		t.Error("Mermaid page does not preserve the exact original source bytes")
	}
}

func TestRenderPageKeepsUntrustedMarkdownInertAndEscapesServiceText(t *testing.T) {
	source := []byte("<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>\n\n[bad](javascript:alert%281%29) [data](data:text/html;base64,PHNjcmlwdD4=) [file](file:///etc/passwd)\n\n![svg](data:image/svg+xml,%3Csvg%20onload%3Dalert(1)%3E)\n")
	page, err := RenderPage(`<project>`, `report&name`, source)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	contents := string(page)
	for _, active := range []string{
		"<script>alert(1)</script>",
		"<img src=\"x\" onerror=",
		`href="javascript:`,
		`href="data:`,
		`href="file:`,
		`src="data:image/svg+xml`,
	} {
		if strings.Contains(contents, active) {
			t.Errorf("rendered page contains active untrusted content %q", active)
		}
	}
	if !strings.Contains(contents, "&lt;project&gt;") || !strings.Contains(contents, "report&amp;name") {
		t.Error("service-owned title text was not HTML-escaped")
	}
	if !strings.Contains(contents, "id=\"markdown-source-data\"") {
		t.Error("source disclosure is missing")
	}
}
