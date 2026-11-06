package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"projectos/kernel/internal/domain"
)

func TestEventStreamResumesFromCursor(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "obj-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeProjectMeta(dir, defaultProjectID); err != nil {
		t.Fatal(err)
	}
	st := seedGoal(t, dir)
	for _, kind := range []string{"started", "accepted"} {
		if err := st.AppendEvent("T1", kind, "detail"); err != nil {
			t.Fatal(err)
		}
	}
	events, err := st.EventsPage(0, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = %+v %v", events, err)
	}
	st.Close()

	ws := newWorkspace(root)
	if p, ok, err := ws.find("obj-1"); err != nil || !ok {
		t.Fatalf("find objective: %+v %v %v", p, ok, err)
	}
	mux, _, err := buildMux(ws, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(mux)
	defer ts.Close()

	first := readSSEEvent(t, ts.URL, "obj-1", "&after="+strconv.FormatInt(events[0].Seq, 10))
	if first.Kind != "accepted" || first.Seq != events[1].Seq {
		t.Fatalf("first reconnect event = %+v", first)
	}

	st, err = openGoalStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent("T1", "blocked", "late"); err != nil {
		st.Close()
		t.Fatal(err)
	}
	all, _ := st.EventsPage(0, 10)
	latest := all[len(all)-1]
	st.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/events/stream?objective=obj-1", nil)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(events[1].Seq, 10))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(res.Body)
	deadline := time.After(3 * time.Second)
	lineCh := make(chan string, 1)
	go func() {
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				lineCh <- scanner.Text()
				return
			}
		}
	}()
	select {
	case line := <-lineCh:
		var ev domain.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Seq != latest.Seq || ev.Kind != "blocked" {
			t.Fatalf("replayed event = %+v", ev)
		}
	case <-deadline:
		t.Fatal("stream did not replay the missed event")
	}
}

func readSSEEvent(t *testing.T, base, objective, query string) domain.Event {
	t.Helper()
	res, err := http.Get(base + "/api/events/stream?objective=" + objective + query)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		t.Fatalf("stream status %d: %s", res.StatusCode, raw)
	}
	scanner := bufio.NewScanner(res.Body)
	ch := make(chan domain.Event, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var ev domain.Event
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil {
					ch <- ev
				}
				return
			}
		}
	}()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no SSE event")
		return domain.Event{}
	}
}
