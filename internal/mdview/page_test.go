package mdview

import (
	"encoding/base64"
	"html"
	"strings"
	"testing"
)

func TestRenderPageSupportsGFMAndSelectableOriginalSource(t *testing.T) {
	source := []byte("# Release report\r\n\r\n| Name | Status |\r\n| --- | --- |\r\n| API | ready |\r\n\r\n[API docs](https://example.test/docs)\r\n\r\n- one\r\n- two\r\n\r\n```go\r\nfmt.Println(\"ok\")\r\n```\r\n")
	page, hasPlantUML, err := RenderPage("demo", "report", source)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if hasPlantUML {
		t.Error("ordinary Markdown page reported PlantUML content")
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
		".site-header { position: sticky; top: 0; z-index: 10;",
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
	if strings.Contains(contents, "mermaid.tiny.min.js") || strings.Contains(contents, "window.mermaid") || strings.Contains(contents, "plantuml-frame.html") {
		t.Error("ordinary Markdown page loaded a diagram renderer")
	}
	encoded := base64.StdEncoding.EncodeToString(source)
	if !strings.Contains(contents, `id="markdown-source-data" type="hidden" value="`+encoded+`"`) {
		t.Error("page source payload does not preserve the exact original bytes as Base64")
	}
}

func TestRenderPageSupportsMermaidAndPreservesFallbackSource(t *testing.T) {
	source := []byte("# System overview\n\n```mermaid\nflowchart TD\n  A[Client] --> B[API]\n```\n\n```mermaid\nsequenceDiagram\n  Client->>API: request\n```\n\n```mermaid\nclassDiagram\n  class Artifact\n```\n\n```mermaid\nnot a valid diagram\n```\n\nThe rest of the document remains readable.\n")
	page, hasPlantUML, err := RenderPage("demo", "overview", source)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if hasPlantUML {
		t.Error("Mermaid-only page reported PlantUML content")
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
	page, hasPlantUML, err := RenderPage(`<project>`, `report&name`, source)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if hasPlantUML {
		t.Error("inert Markdown page reported PlantUML content")
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

func TestRenderPageSupportsPlantUMLAndPreservesFallbackSource(t *testing.T) {
	source := []byte("# Architecture\r\n\r\n```plantuml\r\n@startuml\r\nAlice -> Bob: request\r\nBob --> Alice: response\r\n@enduml\r\n```\r\n\r\n```puml\n@startuml\nclass Artifact\nArtifact --> Project\n@enduml\n```\n\n```plantuml\nnot valid PlantUML\n!includeurl https://evil.example/remote\n```\n\nThe rest remains readable.\r\n")
	page, hasPlantUML, err := RenderPage("demo", "architecture", source)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if !hasPlantUML {
		t.Fatal("PlantUML page did not report PlantUML content")
	}
	contents := string(page)
	for _, want := range []string{
		`<code class="language-plantuml">`,
		`<code class="language-puml">`,
		`frame.src = "/assets/plantuml-frame.html?v=8"`,
		`frame.setAttribute("sandbox", "allow-scripts")`,
		`.plantuml-renderer-pending { position: fixed`,
		`PlantUML preview unavailable; source is shown below.`,
		`@startuml`,
		`not valid PlantUML`,
		`The rest remains readable.`,
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("rendered PlantUML page does not contain %q", want)
		}
	}
	appendFrameToWrapper := strings.Index(contents, "wrapper.append(frame);")
	insertWrapperBeforeSource := strings.Index(contents, "pre.parentNode.insertBefore(wrapper, pre);")
	registerFrame := strings.Index(contents, "pending.set(frame.contentWindow, {frame, wrapper, pre, source, timeout});")
	navigateFrame := strings.Index(contents, `frame.src = "/assets/plantuml-frame.html?v=8"`)
	if appendFrameToWrapper < 0 || insertWrapperBeforeSource < 0 || registerFrame < 0 || navigateFrame < 0 || appendFrameToWrapper > insertWrapperBeforeSource || insertWrapperBeforeSource > registerFrame || registerFrame > navigateFrame {
		t.Error("PlantUML iframe must be attached at the source position and registered before navigation so its WindowProxy is available for messaging")
	}
	if !strings.Contains(contents, "item.pre.remove();") {
		t.Error("PlantUML render must reveal the existing wrapper in place without moving and reloading its iframe")
	}
	if strings.Contains(contents, "plantuml.com/plantuml") {
		t.Error("PlantUML page references an unapproved remote renderer")
	}
	if strings.Contains(contents, `sandbox="allow-scripts allow-same-origin"`) || strings.Contains(contents, `sandbox="allow-same-origin`) {
		t.Error("PlantUML iframe weakens isolation with allow-same-origin")
	}
	if !strings.Contains(contents, `frame.setAttribute("referrerpolicy", "no-referrer")`) {
		t.Error("PlantUML iframe does not suppress referrers")
	}
	encoded := base64.StdEncoding.EncodeToString(source)
	if !strings.Contains(html.UnescapeString(contents), `id="markdown-source-data" type="hidden" value="`+encoded+`"`) {
		t.Error("PlantUML page does not preserve exact original source bytes")
	}
}
