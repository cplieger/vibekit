package fileutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"vibekit/internal/api"
)

// ServeJSONFile registers GET/PUT handlers for a JSON file at
// /api/<basename>. The path must end in ".json" and the fallback must
// itself be valid JSON; both are enforced at registration time via
// panic so wiring mistakes fail at startup.
//
// GET returns the file's contents, or the fallback with HTTP 200 when
// the file doesn't exist yet. Any other read error returns HTTP 500.
//
// PUT accepts a JSON object (arrays and scalars are rejected as
// "expected json object"), round-trips the body through
// json.MarshalIndent before persistence (byte-exact preservation is
// not guaranteed), and enforces MaxJSONBody as the maximum request
// size. Oversized bodies return HTTP 413; malformed JSON returns 400.
func ServeJSONFile(mux *http.ServeMux, path, fallback string, perm os.FileMode) {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext != ".json" {
		panic("ServeJSONFile: path must end in .json, got " + path)
	}
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		panic("ServeJSONFile: cannot derive route name from path " + path)
	}
	if !json.Valid([]byte(fallback)) {
		panic("ServeJSONFile: fallback is not valid JSON for path " + path)
	}
	var mu sync.Mutex
	mux.HandleFunc("/api/"+name, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			serveJSONGet(w, path, name, fallback)
		case http.MethodPut:
			serveJSONPut(w, r, path, name, &mu, perm)
		default:
			api.MethodNotAllowed(w)
		}
	})
}

// serveJSONGet handles the GET branch of ServeJSONFile.
func serveJSONGet(w http.ResponseWriter, path, name, fallback string) {
	api.JSONHeaders(w)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, werr := w.Write([]byte(fallback)); werr != nil {
				slog.Debug("serveJSONFile: fallback write failed",
					"route", name, "path", path, "error", werr)
			}
			return
		}
		slog.Warn("serveJSONFile: read failed",
			"route", name, "path", path, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{api.JSONKeyError: "read failed"})
		return
	}
	if _, werr := w.Write(data); werr != nil {
		slog.Debug("serveJSONFile: write failed",
			"route", name, "path", path, "error", werr)
	}
}

// serveJSONPut handles the PUT branch of ServeJSONFile.
func serveJSONPut(w http.ResponseWriter, r *http.Request, path, name string, mu *sync.Mutex, perm os.FileMode) {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "application/json" {
			slog.Warn("serveJSONFile: unexpected content-type",
				"route", name, "content_type", ct)
			api.BadRequest(w, "expected application/json")
			return
		}
	}
	api.LimitBody(w, r, api.MaxJSONBody)
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if maxErr, ok := errors.AsType[*http.MaxBytesError](err); ok {
			slog.Warn("serveJSONFile: body too large",
				"route", name,
				"limit", api.MaxJSONBody,
				"content_length", r.Header.Get("Content-Length"),
				"error", maxErr)
			api.WriteJSONStatus(w, http.StatusRequestEntityTooLarge,
				map[string]string{api.JSONKeyError: "request body too large"})
			return
		}
		slog.Warn("serveJSONFile: invalid json",
			"route", name, "error", err)
		api.BadRequest(w, "invalid json")
		return
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		api.BadRequest(w, "expected json object")
		return
	}
	if err := SaveJSON(path, mu, body, "serveJSONFile:"+name, perm); err != nil {
		slog.Error("serveJSONFile: save failed",
			"route", name, "path", path, "error", err)
		api.WriteJSONStatus(w, http.StatusInternalServerError,
			map[string]string{api.JSONKeyError: "save failed"})
		return
	}
	slog.Info("serveJSONFile: saved",
		"route", name, "bytes", len(body))
	api.Ok(w)
}
