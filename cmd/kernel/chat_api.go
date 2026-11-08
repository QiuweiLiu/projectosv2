package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"projectos/kernel/internal/chat"
	"projectos/kernel/internal/conversation"
	"projectos/kernel/internal/modelcfg"
)

func registerChatRoutes(mux *http.ServeMux, ctl *controlServer, mgr *chat.Manager, ws *workspace) {
	mux.HandleFunc("/api/conversations", func(w http.ResponseWriter, r *http.Request) {
		if !ctl.authorised(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			list, err := mgr.List(100)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if list == nil {
				list = []conversation.Summary{}
			}
			_ = writeJSON(w, list)
		case http.MethodPost:
			var req struct {
				Title       string `json:"title"`
				ProjectID   string `json:"projectId"`
				ObjectiveID string `json:"objectiveId"`
				Model       string `json:"model"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			dir := ""
			projectID := strings.TrimSpace(req.ProjectID)
			if req.ObjectiveID != "" {
				p, ok, err := ws.find(req.ObjectiveID)
				if err != nil || !ok {
					http.Error(w, "unknown objective", http.StatusNotFound)
					return
				}
				dir = p.Dir
				projectID = p.ProjectID
			}
			if projectID == "" {
				projectID = defaultProjectID
			}
			if _, ok, err := ws.FindProject(projectID); err != nil || !ok {
				http.Error(w, "unknown project", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(req.Model) == "" {
				req.Model = defaultChatModel(ws.root)
			}
			c, err := mgr.CreateConversation(req.Title, projectID, req.ObjectiveID, dir, req.Model)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_ = writeJSON(w, c)
		default:
			http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/conversations/", func(w http.ResponseWriter, r *http.Request) {
		if !ctl.authorised(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, action, ok := splitConversationPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.Method == http.MethodGet && action == "":
			chatGet(w, r, mgr, id)
		case r.Method == http.MethodPost && action == "messages":
			chatSend(w, r, mgr, id)
		case r.Method == http.MethodPost && action == "stop":
			if err := mgr.Stop(r.Context(), id); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			_ = writeJSON(w, map[string]string{"status": "interrupted"})
		case r.Method == http.MethodGet && action == "events":
			chatEvents(w, r, mgr, id)
		case strings.HasPrefix(action, "handoff"):
			chatHandoff(w, r, ctl, mgr, id, action, ws)
		case r.Method == http.MethodPost && strings.HasPrefix(action, "permissions/"):
			chatPermission(w, r, mgr, id, strings.TrimPrefix(action, "permissions/"))
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	})
}

func splitConversationPath(path string) (id, action string, ok bool) {
	rest := strings.TrimPrefix(path, "/api/conversations/")
	if rest == "" || rest == path {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 2)
	id = parts[0]
	if id == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		action = parts[1]
	}
	return id, action, true
}

func defaultChatModel(root string) string {
	s := modelcfg.Load(root)
	if s.Chat != "" {
		return s.Chat
	}
	if s.Worker != "" {
		return s.Worker
	}
	return defaultModel
}

func chatGet(w http.ResponseWriter, r *http.Request, mgr *chat.Manager, id string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	before := r.URL.Query().Get("before")
	c, msgs, err := mgr.Get(r.Context(), id, limit, before)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.ID == "" {
		http.Error(w, "unknown conversation", http.StatusNotFound)
		return
	}
	if msgs == nil {
		msgs = []conversation.Message{}
	}
	_ = writeJSON(w, map[string]any{
		"conversation": c, "messages": msgs, "active": mgr.Active(id),
	})
}

func chatSend(w http.ResponseWriter, r *http.Request, mgr *chat.Manager, id string) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	msg, err := mgr.Send(r.Context(), id, req.Text)
	if err != nil {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, chat.ErrBusy):
			code = http.StatusConflict
		case errors.Is(err, chat.ErrNoServer):
			code = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = writeJSON(w, map[string]any{"message": msg, "status": "running"})
}

func chatPermission(w http.ResponseWriter, r *http.Request, mgr *chat.Manager, id, requestID string) {
	if strings.TrimSpace(requestID) == "" {
		http.Error(w, "request id is required", http.StatusBadRequest)
		return
	}
	var req struct {
		Decision string `json:"decision"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := mgr.ReplyPermission(r.Context(), id, requestID, req.Decision, req.Message); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = writeJSON(w, map[string]string{"status": "ok"})
}

func chatEvents(w http.ResponseWriter, r *http.Request, mgr *chat.Manager, id string) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after == 0 {
		if raw := r.Header.Get("Last-Event-ID"); raw != "" {
			after, _ = strconv.ParseInt(raw, 10, 64)
		}
	}
	ch, done, gap, cancel, err := mgr.Subscribe(id, after)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cancel()
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
	resync := func(reason string) {
		raw, _ := json.Marshal(map[string]string{"reason": reason})
		fmt.Fprintf(w, "event: resync\ndata: %s\n\n", raw)
		flusher.Flush()
	}
	if gap {
		resync("events_pruned_or_replay_too_large")
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-done:
			resync("slow_consumer")
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			raw, _ := json.Marshal(map[string]any{
				"seq": ev.Seq, "kind": ev.Kind, "data": ev.Data, "at": ev.At,
			})
			if ev.Seq > 0 {
				fmt.Fprintf(w, "id: %d\n", ev.Seq)
			}
			fmt.Fprintf(w, "data: %s\n\n", raw)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// chatHandoff turns multi-turn discussion into an editable Goal Draft. Draft
// extraction is intentionally separate from Objective creation: only the
// explicit confirm endpoint creates a Kernel objective, and only a separate
// plan confirmation allows execution.
func chatHandoff(w http.ResponseWriter, r *http.Request, ctl *controlServer, mgr *chat.Manager, conversationID, action string, ws *workspace) {
	rest := strings.TrimPrefix(action, "handoff")
	rest = strings.TrimPrefix(rest, "/")
	switch {
	case rest == "" && r.Method == http.MethodPost:
		d, err := mgr.DraftGoal(r.Context(), conversationID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		_ = writeJSON(w, d)
	case rest == "" && r.Method == http.MethodGet:
		d, ok, err := mgr.LatestDraft(conversationID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "no goal draft", http.StatusNotFound)
			return
		}
		_ = writeJSON(w, d)
	case strings.HasSuffix(rest, "/confirm") && r.Method == http.MethodPost:
		draftID := strings.TrimSuffix(rest, "/confirm")
		chatConfirmHandoff(w, r, ctl, mgr, conversationID, draftID, ws)
	case r.Method == http.MethodPut && rest != "":
		chatUpdateHandoff(w, r, mgr, conversationID, rest)
	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func chatUpdateHandoff(w http.ResponseWriter, r *http.Request, mgr *chat.Manager, conversationID, draftID string) {
	old, ok, err := mgr.GetDraft(draftID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok || old.ConversationID != conversationID {
		http.Error(w, "unknown draft", http.StatusNotFound)
		return
	}
	if old.Status != "draft" {
		http.Error(w, "draft is already confirmed", http.StatusConflict)
		return
	}
	var req struct {
		Goal            string   `json:"goal"`
		Constraints     []string `json:"constraints"`
		ExpectedOutputs []string `json:"expectedOutputs"`
		Acceptance      []string `json:"acceptance"`
		Decisions       []string `json:"decisions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Goal) == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}
	old.Goal = strings.TrimSpace(req.Goal)
	old.Constraints = req.Constraints
	old.ExpectedOutputs = req.ExpectedOutputs
	old.Acceptance = req.Acceptance
	old.Decisions = req.Decisions
	if err := mgr.UpdateDraft(old); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = writeJSON(w, old)
}

func chatConfirmHandoff(w http.ResponseWriter, r *http.Request, ctl *controlServer, mgr *chat.Manager, conversationID, draftID string, ws *workspace) {
	d, ok, err := mgr.GetDraft(draftID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok || d.ConversationID != conversationID {
		http.Error(w, "unknown draft", http.StatusNotFound)
		return
	}
	if d.Status != "draft" {
		http.Error(w, "draft is already confirmed", http.StatusConflict)
		return
	}
	constraints := append([]string(nil), d.Constraints...)
	if len(d.Decisions) > 0 {
		constraints = append(constraints, "Already decided: "+strings.Join(d.Decisions, "; "))
	}
	req := goalRequest{
		Goal: d.Goal, Title: firstLine(d.Goal, 48), ProjectID: d.ProjectID,
		Constraints: strings.Join(constraints, "; "),
		Expected:    strings.Join(d.ExpectedOutputs, "; "),
		Acceptance:  strings.Join(d.Acceptance, "; "),
	}
	objectiveID, err := ctl.createObjective(req)
	if err != nil {
		http.Error(w, "create objective: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := mgr.ConfirmDraft(draftID, objectiveID); err != nil {
		http.Error(w, "link draft: "+err.Error(), http.StatusInternalServerError)
		return
	}
	p, ok, err := ctl.ws.find(objectiveID)
	if err != nil || !ok {
		http.Error(w, "objective created but not found", http.StatusInternalServerError)
		return
	}
	args := []string{"goal", "plan", "--dir", p.Dir, "--planner", "model"}
	if contextText := ws.ProjectContextFor(p.ProjectID, d.Goal); contextText != "" {
		args = append(args, "--context", contextText)
	}
	planErr := ""
	if _, err := ctl.runGoal(p.Dir, args...); err != nil {
		planErr = err.Error()
	}
	ctl.set(objectiveID, map[bool]string{true: "plan failed", false: "planned"}[planErr != ""])
	_ = writeJSON(w, map[string]any{
		"objective": objectiveID, "status": "draft", "planGenerated": planErr == "",
		"planError": planErr,
	})
}
