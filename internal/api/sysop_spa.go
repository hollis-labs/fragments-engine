package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	sysop "github.com/hollis-labs/fragments-engine/apps/sysop"
)

const sysopBasePath = "/sysop"

const sysopPlaceholderHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Fragments Engine Sysop</title>
  <style>
    body { margin: 0; background: #0a0a0a; color: #e5e7eb; font-family: ui-sans-serif, system-ui, -apple-system, sans-serif; }
    main { display: flex; align-items: center; justify-content: center; min-height: 100vh; padding: 24px; text-align: center; }
    h1 { margin: 0; font-size: 1.5rem; font-weight: 600; }
    p { margin: 0.75rem 0 0; color: #9ca3af; }
    code { color: #f9fafb; }
  </style>
</head>
<body>
  <main>
    <div>
      <h1>Sysop UI not built</h1>
      <p>Run <code>make sysop-build</code> and rebuild the Fragments Engine binary.</p>
    </div>
  </main>
</body>
</html>`

func newSysopSPAHandler() http.Handler {
	distFS, err := sysop.DistFS()
	if err != nil {
		return http.HandlerFunc(serveSysopPlaceholder)
	}
	return newSysopSPAHandlerFS(sysopBasePath, distFS)
}

func newSysopSPAHandlerFS(basePath string, distFS fs.FS) http.Handler {
	if _, err := fs.ReadFile(distFS, "index.html"); err != nil {
		return http.HandlerFunc(serveSysopPlaceholder)
	}

	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := strings.TrimPrefix(r.URL.Path, basePath)
		reqPath = strings.TrimPrefix(reqPath, "/")
		if reqPath == "" {
			reqPath = "index.html"
		}

		if _, err := fs.Stat(distFS, reqPath); err != nil {
			if path.Ext(reqPath) != "" {
				http.NotFound(w, r)
				return
			}
			reqPath = "index.html"
		}

		clone := r.Clone(r.Context())
		if reqPath == "index.html" {
			clone.URL.Path = "/"
		} else {
			clone.URL.Path = "/" + reqPath
		}
		fileServer.ServeHTTP(w, clone)
	})
}

func serveSysopPlaceholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(sysopPlaceholderHTML))
}
