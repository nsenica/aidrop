package mdview

import (
	"embed"
	"io/fs"
)

//go:embed assets/mermaid.tiny.min.js
var mermaidJavaScript []byte

//go:embed assets/plantuml/*.js
var plantUMLAssets embed.FS

// PlantUMLAssetVersion identifies the pinned browser renderer bundle.
const PlantUMLAssetVersion = "1.2026.9"

// MermaidJavaScript returns the locally bundled Mermaid Tiny renderer.
func MermaidJavaScript() []byte {
	return mermaidJavaScript
}

// PlantUMLAsset returns a bundled PlantUML runtime asset by file name.
func PlantUMLAsset(name string) ([]byte, bool) {
	if !fs.ValidPath(name) {
		return nil, false
	}
	switch name {
	case "plantuml.js", "viz-global.js", "themes.js", "emoji.js", "openiconic.js":
	default:
		return nil, false
	}
	asset, err := plantUMLAssets.ReadFile("assets/plantuml/" + name)
	return asset, err == nil
}
