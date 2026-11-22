package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"projectos/kernel/internal/chat"
	"projectos/kernel/internal/conversation"
	"projectos/kernel/internal/goal"
	"projectos/kernel/internal/modelcfg"
	"projectos/kernel/internal/opencodeserver"
)

func registerChatRoutes(mux *http.ServeMux, ctl *controlServer, mgr *chat.Manager, ws *workspace) {
	mux.HandleFunc("/api/approval-rules", func(w http.ResponseWriter, r *http.Request) {
		if !ctl.authorised(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			rules := mgr.ApprovalRules()
			if rules == nil {
				rules = []chat.ApprovalRule{}
			}
			_ = writeJSON(w, map[string]any{"rules": rules})
		case http.MethodDelete:
			var req struct {
				Index int `json:"index"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			ok, err := mgr.DeleteApprovalRule(req.Index)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, "unknown rule", http.StatusNotFound)
				return
			}
			_ = writeJSON(w, map[string]bool{"deleted": true})
		default:
			http.Error(w, "GET or DELETE only", http.StatusMethodNotAllowed)
		}
	})
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
		case r.Method == http.MethodPost && action == "detach-objective":
			if err := mgr.DetachObjective(id); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			_ = writeJSON(w, map[string]string{"status": "detached"})
		case r.Method == http.MethodPost && action == "rewind":
			var req struct {
				MessageID string `json:"messageId"`
				LocalOnly bool   `json:"localOnly"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			res, err := mgr.Rewind(r.Context(), id, req.MessageID, req.LocalOnly)
			if err != nil {
				if errors.Is(err, chat.ErrRemoteGone) {
					http.Error(w, "rewind_local_only", http.StatusConflict)
					return
				}
				code := http.StatusBadRequest
				if errors.Is(err, chat.ErrBusy) {
					code = http.StatusConflict
				}
				http.Error(w, err.Error(), code)
				return
			}
			detached := false
			if c, _, gerr := mgr.Get(r.Context(), id, 1, ""); gerr == nil && c.ObjectiveID != "" {
				if p, ok, ferr := ws.find(c.ObjectiveID); ferr == nil && ok &&
					(p.Obj.Status == goal.StatusCancelled || p.Obj.Status == goal.StatusFailed) {
					if derr := mgr.DetachObjective(id); derr == nil {
						detached = true
					}
				}
			}
			_ = writeJSON(w, map[string]any{
				"restoredText": res.RestoredText, "removed": res.Removed,
				"session": res.Session, "remoteForked": res.RemoteForked, "detached": detached,
			})
		case r.Method == http.MethodPost && action == "stop":
			if err := mgr.Stop(r.Context(), id); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			_ = writeJSON(w, map[string]string{"status": "interrupted"})
		case r.Method == http.MethodGet && action == "events":
			chatEvents(w, r, mgr, id)
		case r.Method == http.MethodGet && action == "forms":
			forms, err := mgr.Forms(r.Context(), id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			if forms == nil {
				forms = []opencodeserver.Form{}
			}
			_ = writeJSON(w, map[string]any{"forms": forms})
		case r.Method == http.MethodPost && strings.HasPrefix(action, "forms/"):
			formID := strings.TrimPrefix(action, "forms/")
			var req struct {
				Answer map[string]any `json:"answer"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			if err := mgr.ReplyForm(r.Context(), id, formID, req.Answer); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = writeJSON(w, map[string]string{"status": "ok"})
		case strings.HasPrefix(action, "handoff"):
			chatHandoff(w, r, ctl, mgr, id, action, ws)
		case r.Method == http.MethodPost && action == "auto-approve":
			var req struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
				http.Error(w, "bad json", http.StatusBadRequest)
				return
			}
			if err := mgr.SetAutoApprove(id, req.Enabled); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			_ = writeJSON(w, map[string]any{"enabled": req.Enabled})
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
	lastSeq, _ := mgr.LastEventSeq(id)
	pending, _ := mgr.PendingPermissions(r.Context(), id)
	if pending == nil {
		pending = []opencodeserver.PermissionRequest{}
	}
	_ = writeJSON(w, map[string]any{
		"conversation": c, "messages": msgs, "active": mgr.Active(id),
		"lastEventSeq": lastSeq, "permissions": pending,
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
		Decision  string   `json:"decision"`
		Message   string   `json:"message"`
		Action    string   `json:"action"`
		Resources []string `json:"resources"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := mgr.ReplyPermission(r.Context(), id, requestID, req.Decision, req.Message, req.Action, req.Resources); err != nil {
		if errors.Is(err, opencodeserver.ErrPermissionNotFound) {
			http.Error(w, "permission not found", http.StatusNotFound)
			return
		}
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
	case rest == "start" && r.Method == http.MethodPost:
		chatStartHandoff(w, r, mgr, conversationID)
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
		Goal            string                    `json:"goal"`
		Constraints     []string                  `json:"constraints"`
		ExpectedOutputs []string                  `json:"expectedOutputs"`
		Acceptance      []string                  `json:"acceptance"`
		Decisions       []string                  `json:"decisions"`
		Budget          *conversation.DraftBudget `json:"budget"`
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
	old.Budget = req.Budget
	if err := mgr.UpdateDraft(old); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_ = writeJSON(w, old)
}

// chatStartHandoff turns a control phrase typed into the chat into a goal
// draft: the message is kept in the transcript, and the draft wizard opens in
// the stream instead of a modal.
func chatStartHandoff(w http.ResponseWriter, r *http.Request, mgr *chat.Manager, conversationID string) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	if len(text) > 500 {
		http.Error(w, "text is too long", http.StatusBadRequest)
		return
	}
	c, _, err := mgr.Get(r.Context(), conversationID, 1, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.Status == conversation.StatusRunning {
		http.Error(w, "conversation is running; stop it before starting a handoff", http.StatusConflict)
		return
	}
	if c.ObjectiveID != "" {
		http.Error(w, "conversation already has an objective", http.StatusConflict)
		return
	}
	msg, err := mgr.AppendUserNote(conversationID, text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	d, err := mgr.DraftGoal(r.Context(), conversationID)
	if err != nil {
		http.Error(w, "goal draft: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	_ = writeJSON(w, map[string]any{"message": msg, "draft": d})
}

func chatConfirmHandoff(w http.ResponseWriter, r *http.Request, ctl *controlServer, mgr *chat.Manager, conversationID, draftID string, ws *workspace) {
	if c, _, err := mgr.Get(r.Context(), conversationID, 1, ""); err == nil && c.Status == conversation.StatusRunning {
		http.Error(w, "conversation is running; stop it before confirming handoff", http.StatusConflict)
		return
	}
	d, ok, err := mgr.GetDraft(draftID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok || d.ConversationID != conversationID {
		http.Error(w, "unknown draft", http.StatusNotFound)
		return
	}
	confirmKey := "handoff:" + draftID
	if !ctl.claim(confirmKey) {
		http.Error(w, "handoff confirmation is already in progress", http.StatusConflict)
		return
	}
	defer ctl.release(confirmKey)

	var objectiveID string
	switch d.Status {
	case "draft":
		objectiveID = newObjectiveID()
		claimed, err := mgr.ClaimDraft(draftID, objectiveID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !claimed {
			http.Error(w, "draft is being confirmed", http.StatusConflict)
			return
		}
	case "confirming", "confirmed":
		objectiveID = d.ObjectiveID
		if objectiveID == "" {
			http.Error(w, "draft is being confirmed without a persisted objective id; inspect the workspace", http.StatusConflict)
			return
		}
	default:
		http.Error(w, "draft is not confirmable", http.StatusConflict)
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

	p, found, err := ctl.ws.find(objectiveID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		if d.Status == "confirmed" {
			http.Error(w, "confirmed handoff points at a missing objective", http.StatusInternalServerError)
			return
		}
		created, err := ctl.createObjectiveWithID(req, objectiveID)
		if err != nil {
			_, found, findErr := ctl.ws.find(objectiveID)
			if findErr != nil {
				http.Error(w, "create objective: "+err.Error()+" (cannot verify whether objective "+objectiveID+" exists; retry)", http.StatusInternalServerError)
				return
			}
			if found {
				http.Error(w, "create objective: "+err.Error()+" (objective "+objectiveID+" exists; retry to link it)", http.StatusInternalServerError)
				return
			}
			_ = mgr.ReleaseDraft(draftID)
			http.Error(w, "create objective: "+err.Error(), http.StatusBadRequest)
			return
		}
		objectiveID = created
	}
	if d.Status != "confirmed" {
		if err := mgr.ConfirmDraft(draftID, objectiveID); err != nil {
			http.Error(w, "link draft: "+err.Error()+" (objective "+objectiveID+" created)", http.StatusInternalServerError)
			return
		}
	}
	p, found, err = ctl.ws.find(objectiveID)
	if err != nil || !found {
		http.Error(w, "objective created but not found", http.StatusInternalServerError)
		return
	}

	// A retry after the plan already exists is idempotent: return the objective
	// instead of creating a second one or planning it twice.
	if p.Obj.Status != goal.StatusDraft || p.Obj.PlanVersion > 0 {
		_ = writeJSON(w, map[string]any{
			"objective": objectiveID, "status": string(p.Obj.Status), "planPending": false, "budget": d.Budget,
		})
		return
	}

	// Planning can take a minute or two. Create the objective and return now;
	// the flow card polls the objective and the job status, so the chat is never
	// stuck on a disabled "creating..." button.
	if !ctl.claim(objectiveID) {
		_ = writeJSON(w, map[string]any{"objective": objectiveID, "status": "draft",
			"planPending": false, "planError": "another action is already running", "budget": d.Budget})
		return
	}
	args := []string{"goal", "plan", "--dir", p.Dir, "--planner", "model"}
	if contextText := ws.ProjectContextFor(p.ProjectID, d.Goal); contextText != "" {
		args = append(args, "--context", contextText)
	}
	ctl.set(objectiveID, "planning")
	go func() {
		defer ctl.release(objectiveID)
		if _, err := ctl.runGoal(p.Dir, args...); err != nil {
			ctl.set(objectiveID, "plan failed: "+err.Error())
			return
		}
		ctl.set(objectiveID, "planned")
	}()
	_ = writeJSON(w, map[string]any{
		"objective": objectiveID, "status": "draft", "planPending": true, "budget": d.Budget,
	})
}
