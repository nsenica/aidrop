package httpapi

import (
	"strings"
	"testing"
)

func TestArtifactViewerEscapesLabelsAndFramesPublishedContent(t *testing.T) {
	page, err := renderArtifactViewer("demo&team", "report<draft>", "/p/demo/report/?_aidrop_content=1")
	if err != nil {
		t.Fatalf("renderArtifactViewer: %v", err)
	}
	contents := string(page)
	for _, want := range []string{
		"href=\"/\">Home</a>",
		"href=\"/p/demo&amp;team/\">demo&amp;team</a>",
		"report&lt;draft&gt;",
		"src=\"/p/demo/report/?_aidrop_content=1\"",
		"sandbox=\"allow-scripts\"",
	} {
		if !strings.Contains(contents, want) {
			t.Errorf("viewer does not contain %q: %s", want, contents)
		}
	}
	for _, unexpected := range []string{"<script", "<link rel=", `src="https://`, `href="https://`} {
		if strings.Contains(contents, unexpected) {
			t.Errorf("viewer unexpectedly contains %q", unexpected)
		}
	}
	if strings.Contains(contents, "report<draft>") || strings.Contains(contents, "demo&team") {
		t.Errorf("viewer did not escape service labels: %s", contents)
	}
}
