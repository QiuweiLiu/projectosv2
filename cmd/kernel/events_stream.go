package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"projectos/kernel/internal/store"
)

// serveEventStream is the SSE form of the existing cursor API. It polls the
// authoritative event table inside the server, so the browser gets low-latency
// updates without a second event store. `after` makes reconnects idempotent.
func serveEventStream(w http.ResponseWriter, r *http.Request, ws *workspace) {
	p, ok, err := ws.resolve(r.URL.Query().Get("objective"))
	if err != nil || !ok {
		http.Error(w, "objective is required", http.StatusBadRequest)
		return
	}
	st, err := store.OpenReadOnly(filepath.Join(p.Dir, "kernel.db"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer st.Close()
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after == 0 {
		if raw := r.Header.Get("Last-Event-ID"); raw != "" {
			after, _ = strconv.ParseInt(raw, 10, 64)
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		events, err := st.EventsPage(after, 100)
		if err != nil {
			return
		}
		for _, ev := range events {
			raw, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, raw)
			after = ev.Seq
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
