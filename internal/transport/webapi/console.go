package webapi

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"strings"
)

// consoleFiles holds the built Vue console. `npm run build` in web/ writes it
// here, so `go build` produces one binary with the console inside it.
//
//go:embed all:app
var consoleFiles embed.FS

// ConsolePathPrefix is where the console is mounted. It sits beside the
// existing panels rather than replacing them, so nothing in use breaks while
// the console grows.
const ConsolePathPrefix = "/app/"

// NewConsoleHandler serves the built console. Unknown paths under the prefix
// return index.html so the client-side router owns its own routes; a missing
// asset still returns 404 rather than the shell.
//
// The shell itself is public: it has to load before it can show a sign-in form.
// Every byte of data it displays still passes the session check on /api/v1.
func NewConsoleHandler() (http.Handler, error) {
	files, err := fs.Sub(consoleFiles, "app")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(files))

	handler := http.StripPrefix(strings.TrimSuffix(ConsolePathPrefix, "/"),
		http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			requested := strings.TrimPrefix(request.URL.Path, "/")
			if requested == "" {
				serveConsoleIndex(response, files)
				return
			}
			setConsoleSecurityHeaders(response, "")
			if _, err := fs.Stat(files, requested); err != nil {
				// A real asset request must fail as one. Anything else is a
				// client-side route, which the shell resolves.
				if strings.HasPrefix(requested, "assets/") {
					http.NotFound(response, request)
					return
				}
				serveConsoleIndex(response, files)
				return
			}
			fileServer.ServeHTTP(response, request)
		}))
	return handler, nil
}

// serveConsoleIndex stamps a fresh nonce into the shell so PrimeVue can sign
// the styles it injects at runtime, and the policy stays free of
// 'unsafe-inline'.
func serveConsoleIndex(response http.ResponseWriter, files fs.FS) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		http.Error(response, "console is unavailable", http.StatusInternalServerError)
		return
	}
	nonce, err := newNonce()
	if err != nil {
		http.Error(response, "console is unavailable", http.StatusInternalServerError)
		return
	}
	setConsoleSecurityHeaders(response, nonce)
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(bytes.ReplaceAll(index, []byte(noncePlaceholder), []byte(nonce)))
}

const noncePlaceholder = "__CSP_NONCE__"

// 3d-force-graph and its renderer/tooltip dependencies inject these three
// version-locked CSS blocks when the lazy graph chunk loads. The empty hash
// covers creation of their initially empty style elements. Exact hashes keep
// style-src closed: changing any dependency CSS requires an explicit review.
const graphRuntimeStyleSources = " 'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='" +
	" 'sha256-0/4q5IwejFb2zgHlQwwtwmGHS8ZbXE1kmz/TkRFlZ7M='" +
	" 'sha256-yfc2FhpkFR0EAy3T+zDsaAFGXSP9B3ELNvaJKDzNhkk='" +
	" 'sha256-9xjtvxMT1ApHlgn9ohbh2FNfvK5Tqtzy94BjfXBeMSY='"

func newNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// setConsoleSecurityHeaders mirrors the panel headers. The console ships only
// hashed bundles, so 'self' covers scripts, styles, and fonts with no inline
// allowance beyond the style attributes PrimeVue sets on elements.
func setConsoleSecurityHeaders(response http.ResponseWriter, nonce string) {
	styleSource := "'self'" + graphRuntimeStyleSources
	if nonce != "" {
		styleSource += " 'nonce-" + nonce + "'"
	}
	response.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src "+styleSource+"; style-src-attr 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; "+
			"base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
}
