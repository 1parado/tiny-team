package main

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

//go:embed webui.html
var traceHTML string

// RegisterTraceRoutes mounts the live trace UI and control APIs on mux.
//
//	GET  /           timeline page
//	GET  /api/trace  events + running/done status
//	POST /api/run    {"task":"..."} start a run (ctrl required)
//	POST /api/stop   cancel the active run (ctrl required)
//
// When ctrl is nil, /api/run and /api/stop return 501 (read-only viewer mode).
func RegisterTraceRoutes(mux *http.ServeMux, tr *Tracer, ctrl *RunController) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(traceHTML))
	})
	mux.HandleFunc("/api/trace", func(w http.ResponseWriter, r *http.Request) {
		events, done := tr.Snapshot()
		running := false
		lastErr := ""
		if ctrl != nil {
			running = ctrl.Running()
			lastErr = ctrl.LastError()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"events":  events,
			"done":    done,
			"running": running,
			"error":   lastErr,
		})
	})
	mux.HandleFunc("/api/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if ctrl == nil {
			http.Error(w, "run control not enabled", http.StatusNotImplemented)
			return
		}
		var body struct {
			Task string `json:"task"`
		}
		data, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err := json.Unmarshal(data, &body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := ctrl.Start(body.Task); err != nil {
			msg := err.Error()
			code := http.StatusBadRequest
			if strings.Contains(msg, "already running") {
				code = http.StatusConflict
			}
			http.Error(w, msg, code)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "running": true})
	})
	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if ctrl == nil {
			http.Error(w, "run control not enabled", http.StatusNotImplemented)
			return
		}
		stopped := ctrl.Stop()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "stopped": stopped})
	})
}
