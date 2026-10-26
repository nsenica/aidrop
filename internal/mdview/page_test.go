package mdview

import (
	"encoding/base64"
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
	encoded := base64.StdEncoding.EncodeToString(source)
	if !strings.Contains(contents, `id="markdown-source-data" type="hidden" value="`+encoded+`"`) {
		t.Error("page source payload does not preserve the exact original bytes as Base64")
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
