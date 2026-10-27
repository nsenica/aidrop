package mdview

const plantUMLAssetBase = "/assets/plantuml/" + PlantUMLAssetVersion + "/"

// PlantUMLRendererCSP limits the isolated renderer to local scripts and its
// blob-backed worker. The sandbox prevents the renderer from acquiring the
// service origin even when this endpoint is opened directly.
func PlantUMLRendererCSP() string {
	return "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline' 'wasm-unsafe-eval'; style-src 'unsafe-inline'; worker-src blob:; connect-src 'none'; base-uri 'none'; form-action 'none'"
}

// PlantUMLRendererPage returns the small browser-side bridge used by Markdown
// previews. The diagram source is received from the parent and never uploaded.
func PlantUMLRendererPage() []byte {
	return []byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="referrer" content="no-referrer">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>
    html, body { min-height: 100%; margin: 0; background: #fff; }
    #plantuml-output svg { display: block; width: 100%; height: auto; margin: 0 auto; }
  </style>
  <script>window.PLANTUML_STDLIB_BASE = "` + plantUMLAssetBase + `";</script>
  <script src="` + plantUMLAssetBase + `viz-global.js"></script>
  <script type="module">
    const send = message => parent.postMessage(message, "*");
    import("` + plantUMLAssetBase + `plantuml.js").then(({renderToString}) => {
      let used = false;
      const output = document.getElementById("plantuml-output");
      window.addEventListener("message", event => {
        if (event.source !== parent || used || event.data?.type !== "aidrop-plantuml-render") return;
        used = true;
        const source = event.data.source;
        if (typeof source !== "string" || source.length > 50000) {
          send({type:"aidrop-plantuml-error"});
          return;
        }
        try {
          renderToString(source.split(/\r\n|\r|\n/), svgSource => {
            try {
              if (typeof svgSource !== "string" || svgSource.length > 1000000) {
                send({type:"aidrop-plantuml-error"});
                return;
              }
              const parsed = new DOMParser().parseFromString(svgSource, "image/svg+xml");
              if (parsed.querySelector("parsererror") || parsed.documentElement.localName !== "svg") {
                send({type:"aidrop-plantuml-error"});
                return;
              }
              const svg = document.importNode(parsed.documentElement, true);
              const svgText = svg.textContent || "";
              const hasSyntaxError = /PlantUML version\b[\s\S]*Syntax Error\?/i.test(svgText);
              const hasMissingTarget = /missing target/i.test(svgText) && /(?:-->>|-->|->>|->)\s*:/m.test(source);
              if (hasSyntaxError || hasMissingTarget) {
                send({type:"aidrop-plantuml-error"});
                return;
              }
              output.replaceChildren(svg);
              const bounds = svg.getBBox();
              const viewBox = svg.viewBox.baseVal;
              const width = viewBox.width || bounds.width;
              const height = viewBox.height || bounds.height;
              if (width < 1 || height < 1) {
                send({type:"aidrop-plantuml-error"});
                return;
              }
              send({type:"aidrop-plantuml-rendered", width, height});
            } catch (_) {
              send({type:"aidrop-plantuml-error"});
            }
          }, () => send({type:"aidrop-plantuml-error"}), {maxSvgSize:8192});
        } catch (_) {
          send({type:"aidrop-plantuml-error"});
        }
      });
      send({type:"aidrop-plantuml-ready"});
    }).catch(() => send({type:"aidrop-plantuml-error"}));
  </script>
</head>
<body><div id="plantuml-output"></div></body>
</html>
`)
}
