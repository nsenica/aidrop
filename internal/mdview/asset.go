package mdview

import _ "embed"

//go:embed assets/mermaid.tiny.min.js
var mermaidJavaScript []byte

// MermaidJavaScript returns the locally bundled Mermaid Tiny renderer.
func MermaidJavaScript() []byte {
	return mermaidJavaScript
}
