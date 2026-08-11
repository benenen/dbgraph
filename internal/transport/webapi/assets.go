package webapi

import (
	"bytes"
	"embed"
	"html/template"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets/*
var webAssets embed.FS

var pageTemplates = template.Must(template.ParseFS(webAssets, "assets/index.html", "assets/login.html"))

func serveStaticAsset(response http.ResponseWriter, request *http.Request) {
	cleaned := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(request.URL.Path)), "/")
	if !strings.HasPrefix(cleaned, "assets/") {
		http.Error(response, "not found", http.StatusNotFound)
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(cleaned))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if strings.HasSuffix(cleaned, "cytoscape.min.js") {
		response.Header().Set("Cache-Control", "public, max-age=3600")
	}
	serveAsset(response, request, cleaned, contentType)
}

func serveAsset(response http.ResponseWriter, request *http.Request, name string, contentType string) {
	content, err := webAssets.ReadFile(name)
	if err != nil {
		http.Error(response, "not found", http.StatusNotFound)
		return
	}
	response.Header().Set("Content-Type", contentType)
	http.ServeContent(response, request, name, time.Time{}, bytes.NewReader(content))
}

func servePageTemplate(response http.ResponseWriter, name string) {
	var rendered bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&rendered, name, nil); err != nil {
		http.Error(response, "page could not be rendered", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(rendered.Bytes())
}
