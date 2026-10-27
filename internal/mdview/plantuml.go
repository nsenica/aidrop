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
    #plantuml-output { position: fixed; top: 0; left: 0; }
  </style>
  <script>window.PLANTUML_STDLIB_BASE = "` + plantUMLAssetBase + `";</script>
  <script src="` + plantUMLAssetBase + `viz-global.js"></script>
  <script type="module">
    const send = message => parent.postMessage(message, "*");
    import("` + plantUMLAssetBase + `plantuml.js").then(({renderToString}) => {
      const queue = [];
      let rendering = false;
      const output = document.getElementById("plantuml-output");
      const fail = requestId => send({type:"aidrop-plantuml-error", requestId});
      const renderNext = () => {
        if (rendering || queue.length === 0) return;
        rendering = true;
        const request = queue.shift();
        let finished = false;
        const finish = () => {
          if (finished) return;
          finished = true;
          output.replaceChildren();
          rendering = false;
          renderNext();
        };
        if (typeof request.source !== "string" || request.source.length > 50000) {
          fail(request.requestId);
          finish();
          return;
        }
        try {
          renderToString(request.source.split(/\r\n|\r|\n/), svgSource => {
            try {
              if (typeof svgSource !== "string" || svgSource.length > 1000000) {
                fail(request.requestId);
                return;
              }
              const parsed = new DOMParser().parseFromString(svgSource, "image/svg+xml");
              if (parsed.querySelector("parsererror") || parsed.documentElement.localName !== "svg") {
                fail(request.requestId);
                return;
              }
              const svg = document.importNode(parsed.documentElement, true);
              const svgText = svg.textContent || "";
              const hasSyntaxError = /PlantUML version\b[\s\S]*Syntax Error\?/i.test(svgText);
              const hasMissingTarget = /missing target/i.test(svgText) && /(?:-->>|-->|->>|->)\s*:/m.test(request.source);
              if (hasSyntaxError || hasMissingTarget) {
                fail(request.requestId);
                return;
              }
              output.replaceChildren(svg);
              const bounds = svg.getBBox();
              const viewBox = svg.viewBox.baseVal;
              const width = viewBox.width || bounds.width;
              const height = viewBox.height || bounds.height;
              if (width < 1 || height < 1) {
                fail(request.requestId);
                return;
              }
              send({
                type:"aidrop-plantuml-rendered",
                requestId:request.requestId,
                width,
                height,
                svg:new XMLSerializer().serializeToString(svg)
              });
            } catch (_) {
              fail(request.requestId);
            } finally {
              finish();
            }
          }, () => {
            fail(request.requestId);
            finish();
          }, {maxSvgSize:8192});
        } catch (_) {
          fail(request.requestId);
          finish();
        }
      };
      window.addEventListener("message", event => {
        if (event.source !== parent || event.data?.type !== "aidrop-plantuml-render") return;
        if (typeof event.data.requestId !== "number" || !Number.isSafeInteger(event.data.requestId)) return;
        if (queue.length >= 50) {
          fail(event.data.requestId);
          return;
        }
        queue.push({requestId:event.data.requestId, source:event.data.source});
        renderNext();
      });
      send({type:"aidrop-plantuml-ready"});
    }).catch(() => send({type:"aidrop-plantuml-error"}));
  </script>
</head>
<body><div id="plantuml-output"></div></body>
</html>
`)
}
