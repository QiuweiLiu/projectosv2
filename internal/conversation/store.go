// Package conversation persists chat history separately from Kernel task state.
// A conversation may be attached to an objective, but it is never a task and
// never changes Task/Run status.
package conversation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Status string

const (
	StatusIdle        Status = "idle"
	StatusRunning     Status = "running"
	StatusInterrupted Status = "interrupted"
	StatusError       Status = "error"
)

type Conversation struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	ProjectID   string    `json:"projectId,omitempty"`
	ObjectiveID string    `json:"objectiveId,omitempty"`
	Dir         string    `json:"dir,omitempty"`
	SessionID   string    `json:"sessionId,omitempty"`
	Model       string    `json:"model,omitempty"`
	Status      Status    `json:"status"`
	ContextSent bool      `json:"contextSent"`
	LastError   string    `json:"lastError,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	RemoteID       string    `json:"remoteId,omitempty"`
	Role           string    `json:"role"` // user | assistant
	Text           string    `json:"text"`
	CreatedAt      time.Time `json:"createdAt"`
}

type GoalDraft struct {
	ID               string    `json:"id"`
	ConversationID   string    `json:"conversationId"`
	ProjectID        string    `json:"projectId,omitempty"`
	Goal             string    `json:"goal"`
	Constraints      []string  `json:"constraints,omitempty"`
	ExpectedOutputs  []string  `json:"expectedOutputs,omitempty"`
	Acceptance       []string  `json:"acceptance,omitempty"`
	Decisions        []string  `json:"decisions,omitempty"`
	SourceMessageIDs []string  `json:"sourceMessageIds,omitempty"`
	ContextVersion   string    `json:"contextVersion,omitempty"`
	Status           string    `json:"status"` // draft | confirmed
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type Event struct {
	Seq            int64           `json:"seq"`
	ConversationID string          `json:"conversationId"`
	Kind           string          `json:"kind"`
	Data           json.RawMessage `json:"data"`
	At             time.Time       `json:"at"`
}

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS conversations (
	id           TEXT PRIMARY KEY,
	title        TEXT NOT NULL DEFAULT '',
	project_id   TEXT NOT NULL DEFAULT '',
	objective_id TEXT NOT NULL DEFAULT '',
	dir          TEXT NOT NULL DEFAULT '',
	session_id   TEXT NOT NULL DEFAULT '',
	model        TEXT NOT NULL DEFAULT '',
	status       TEXT NOT NULL DEFAULT 'idle',
	context_sent INTEGER NOT NULL DEFAULT 0,
	last_error   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
	id              TEXT PRIMARY KEY,
	conversation_id TEXT NOT NULL,
	remote_id       TEXT NOT NULL DEFAULT '',
	role            TEXT NOT NULL,
	text            TEXT NOT NULL,
	created_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_conversation ON messages(conversation_id, created_at);
CREATE TABLE IF NOT EXISTS events (
	seq             INTEGER PRIMARY KEY AUTOINCREMENT,
	conversation_id TEXT NOT NULL,
	kind            TEXT NOT NULL,
	data            TEXT NOT NULL DEFAULT '{}',
	at              TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_conversation ON events(conversation_id, seq);
CREATE TABLE IF NOT EXISTS goal_drafts (
	id                 TEXT PRIMARY KEY,
	conversation_id    TEXT NOT NULL,
	project_id         TEXT NOT NULL DEFAULT '',
	goal               TEXT NOT NULL,
	constraints        TEXT NOT NULL DEFAULT '[]',
	expected_outputs   TEXT NOT NULL DEFAULT '[]',
	acceptance         TEXT NOT NULL DEFAULT '[]',
	decisions          TEXT NOT NULL DEFAULT '[]',
	source_message_ids TEXT NOT NULL DEFAULT '[]',
	context_version    TEXT NOT NULL DEFAULT '',
	status             TEXT NOT NULL DEFAULT 'draft',
	created_at         TEXT NOT NULL,
	updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_goal_drafts_conversation ON goal_drafts(conversation_id, updated_at);
`

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("conversation store path must not be empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Chat history is private user content; keep it owner-only even when the
	// workspace directory has a broader mode.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Chmod(path+suffix, 0o600)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateConversations(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateMessages(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func migrateConversations(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(conversations)`)
	if err != nil {
		return err
	}
	have := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "context_sent" {
			have = true
		}
	}
	rows.Close()
	if !have {
		_, err := db.Exec(`ALTER TABLE conversations ADD COLUMN context_sent INTEGER NOT NULL DEFAULT 0`)
		return err
	}
	return nil
}

func migrateMessages(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		return err
	}
	have := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "remote_id" {
			have = true
		}
	}
	rows.Close()
	if !have {
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN remote_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_remote
		ON messages(conversation_id, remote_id) WHERE remote_id <> ''`)
	return err
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(raw string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, raw)
	return t
}

func (s *Store) Create(title, projectID, objectiveID, dir, model string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	title = strings.TrimSpace(title)
	if title == "" {
		title = "新对话"
	}
	conversationID := fmt.Sprintf("conv-%d", time.Now().UnixNano())
	ts := now()
	_, err := s.db.Exec(`INSERT INTO conversations
		(id,title,project_id,objective_id,dir,model,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		conversationID, title, projectID, objectiveID, dir, model, string(StatusIdle), ts, ts)
	if err != nil {
		return Conversation{}, err
	}
	return s.getLocked(conversationID)
}

func (s *Store) Get(id string) (Conversation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.getLocked(id)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, false, nil
	}
	if err != nil {
		return Conversation{}, false, err
	}
	return row, true, nil
}

func (s *Store) getLocked(id string) (Conversation, error) {
	var c Conversation
	var created, updated string
	var contextSent int
	err := s.db.QueryRow(`SELECT id,title,project_id,objective_id,dir,session_id,model,status,context_sent,last_error,created_at,updated_at
		FROM conversations WHERE id=?`, id).Scan(
		&c.ID, &c.Title, &c.ProjectID, &c.ObjectiveID, &c.Dir, &c.SessionID, &c.Model,
		&c.Status, &contextSent, &c.LastError, &created, &updated)
	c.ContextSent = contextSent == 1
	if err != nil {
		return Conversation{}, err
	}
	c.CreatedAt, c.UpdatedAt = parseTime(created), parseTime(updated)
	return c, nil
}

// Summary adds the last message preview without loading the whole history.
type Summary struct {
	Conversation
	LastMessage string `json:"lastMessage,omitempty"`
}

func (s *Store) List(limit int) ([]Summary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT c.id,c.title,c.project_id,c.objective_id,c.dir,c.session_id,c.model,
		c.status,c.context_sent,c.last_error,c.created_at,c.updated_at,
		COALESCE((SELECT m.text FROM messages m WHERE m.conversation_id=c.id ORDER BY m.created_at DESC, m.rowid DESC LIMIT 1),'')
		FROM conversations c ORDER BY c.updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var c Summary
		var created, updated string
		var contextSent int
		if err := rows.Scan(&c.ID, &c.Title, &c.ProjectID, &c.ObjectiveID, &c.Dir, &c.SessionID, &c.Model,
			&c.Status, &contextSent, &c.LastError, &created, &updated, &c.LastMessage); err != nil {
			return nil, err
		}
		c.CreatedAt, c.UpdatedAt = parseTime(created), parseTime(updated)
		c.ContextSent = contextSent == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) MarkContextSent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE conversations SET context_sent=1, updated_at=? WHERE id=?`, now(), id)
	return err
}

func (s *Store) SetSession(id, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE conversations SET session_id=?, updated_at=? WHERE id=?`, sessionID, now(), id)
	return err
}

func (s *Store) SetDir(id, dir string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE conversations SET dir=?, updated_at=? WHERE id=?`, dir, now(), id)
	return err
}

func (s *Store) SetModel(id, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE conversations SET model=?, updated_at=? WHERE id=?`, model, now(), id)
	return err
}

func (s *Store) SetStatus(id string, status Status, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE conversations SET status=?, last_error=?, updated_at=? WHERE id=?`,
		string(status), lastError, now(), id)
	return err
}

func (s *Store) Rename(id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE conversations SET title=?, updated_at=? WHERE id=?`, title, now(), id)
	return err
}

func (s *Store) AppendMessage(conversationID, role, text string) (Message, error) {
	return s.AppendMessageWithRemote(conversationID, role, text, "", time.Now().UTC())
}

// AppendMessageWithRemote stores a message with the provider's stable message
// id. Re-syncing the same remote message is idempotent; two different remote
// messages with identical text are both kept.
func (s *Store) AppendMessageWithRemote(conversationID, role, text, remoteID string, at time.Time) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	msg := Message{ID: fmt.Sprintf("msg_%d", time.Now().UnixNano()), ConversationID: conversationID,
		RemoteID: remoteID, Role: role, Text: text, CreatedAt: at.UTC()}
	if remoteID != "" {
		var existingID string
		err := s.db.QueryRow(`SELECT id FROM messages WHERE conversation_id=? AND remote_id=?`,
			conversationID, remoteID).Scan(&existingID)
		if err == nil {
			return s.messageLocked(conversationID, existingID)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Message{}, err
		}
	}
	_, err := s.db.Exec(`INSERT INTO messages (id,conversation_id,remote_id,role,text,created_at)
		VALUES (?,?,?,?,?,?)`,
		msg.ID, msg.ConversationID, msg.RemoteID, msg.Role, msg.Text, msg.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Message{}, err
	}
	_, _ = s.db.Exec(`UPDATE conversations SET updated_at=? WHERE id=?`, now(), conversationID)
	return msg, nil
}

func (s *Store) messageLocked(conversationID, id string) (Message, error) {
	var m Message
	var created string
	err := s.db.QueryRow(`SELECT id,conversation_id,remote_id,role,text,created_at FROM messages
		WHERE conversation_id=? AND id=?`, conversationID, id).Scan(
		&m.ID, &m.ConversationID, &m.RemoteID, &m.Role, &m.Text, &created)
	if err != nil {
		return Message{}, err
	}
	m.CreatedAt = parseTime(created)
	return m, nil
}

func (s *Store) SetRemoteID(localID, remoteID string) error {
	if remoteID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE messages SET remote_id=? WHERE id=?`, remoteID, localID)
	return err
}

func (s *Store) HasMessageID(conversationID, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=? AND id=?`,
		conversationID, id).Scan(&n)
	return n > 0, err
}

// MatchLegacyMessage returns a unique old row that predates remote_id. It is
// used only for backward-compatible sync; new rows always match by remote id.
func (s *Store) MatchLegacyMessage(conversationID, role, text string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id FROM messages
		WHERE conversation_id=? AND role=? AND text=? AND remote_id='' LIMIT 2`,
		conversationID, role, text)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", false, err
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		return "", false, rows.Err()
	}
	return ids[0], true, nil
}

func (s *Store) HasRemoteMessage(conversationID, remoteID string) (bool, error) {
	if remoteID == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=? AND remote_id=?`,
		conversationID, remoteID).Scan(&n)
	return n > 0, err
}

func (s *Store) Messages(conversationID string, limit int, before string) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	query := `SELECT id,conversation_id,remote_id,role,text,created_at FROM messages
		WHERE conversation_id=?`
	args := []any{conversationID}
	if before != "" {
		query += ` AND created_at < ?`
		args = append(args, before)
	}
	query += ` ORDER BY created_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.RemoteID, &m.Role, &m.Text, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTime(created)
		out = append(out, m)
	}
	// Return oldest-first; the query walks backwards for pagination.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *Store) AppendEvent(conversationID, kind string, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`INSERT INTO events (conversation_id,kind,data,at) VALUES (?,?,?,?)`,
		conversationID, kind, string(raw), now())
	if err != nil {
		return Event{}, err
	}
	seq, _ := res.LastInsertId()
	return Event{Seq: seq, ConversationID: conversationID, Kind: kind, Data: raw, At: time.Now().UTC()}, nil
}

func (s *Store) EventsAfter(conversationID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT seq,conversation_id,kind,data,at FROM events
		WHERE conversation_id=? AND seq>? ORDER BY seq LIMIT ?`, conversationID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var data, at string
		if err := rows.Scan(&e.Seq, &e.ConversationID, &e.Kind, &data, &at); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(data)
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) CreateGoalDraft(d GoalDraft) (GoalDraft, error) {
	if strings.TrimSpace(d.ConversationID) == "" || strings.TrimSpace(d.Goal) == "" {
		return GoalDraft{}, fmt.Errorf("goal draft needs conversation and goal")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d.ID = fmt.Sprintf("draft-%d", time.Now().UnixNano())
	if d.Status == "" {
		d.Status = "draft"
	}
	d.CreatedAt, d.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	raw := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	_, err := s.db.Exec(`INSERT INTO goal_drafts
		(id,conversation_id,project_id,goal,constraints,expected_outputs,acceptance,decisions,
		 source_message_ids,context_version,status,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ConversationID, d.ProjectID, d.Goal, raw(d.Constraints), raw(d.ExpectedOutputs),
		raw(d.Acceptance), raw(d.Decisions), raw(d.SourceMessageIDs), d.ContextVersion, d.Status,
		d.CreatedAt.Format(time.RFC3339Nano), d.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return GoalDraft{}, err
	}
	return d, nil
}

func (s *Store) GetGoalDraft(id string) (GoalDraft, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getDraftLocked(id)
}

func (s *Store) LatestGoalDraft(conversationID string) (GoalDraft, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id string
	err := s.db.QueryRow(`SELECT id FROM goal_drafts WHERE conversation_id=?
		ORDER BY updated_at DESC, rowid DESC LIMIT 1`, conversationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalDraft{}, false, nil
	}
	if err != nil {
		return GoalDraft{}, false, err
	}
	return s.getDraftLocked(id)
}

func (s *Store) getDraftLocked(id string) (GoalDraft, bool, error) {
	var d GoalDraft
	var constraints, expected, acceptance, decisions, sources, created, updated string
	err := s.db.QueryRow(`SELECT id,conversation_id,project_id,goal,constraints,expected_outputs,
		acceptance,decisions,source_message_ids,context_version,status,created_at,updated_at
		FROM goal_drafts WHERE id=?`, id).Scan(
		&d.ID, &d.ConversationID, &d.ProjectID, &d.Goal, &constraints, &expected, &acceptance,
		&decisions, &sources, &d.ContextVersion, &d.Status, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalDraft{}, false, nil
	}
	if err != nil {
		return GoalDraft{}, false, err
	}
	_ = json.Unmarshal([]byte(constraints), &d.Constraints)
	_ = json.Unmarshal([]byte(expected), &d.ExpectedOutputs)
	_ = json.Unmarshal([]byte(acceptance), &d.Acceptance)
	_ = json.Unmarshal([]byte(decisions), &d.Decisions)
	_ = json.Unmarshal([]byte(sources), &d.SourceMessageIDs)
	d.CreatedAt, d.UpdatedAt = parseTime(created), parseTime(updated)
	return d, true, nil
}

func (s *Store) UpdateGoalDraft(d GoalDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	res, err := s.db.Exec(`UPDATE goal_drafts SET goal=?,constraints=?,expected_outputs=?,acceptance=?,
		decisions=?,source_message_ids=?,context_version=?,updated_at=? WHERE id=? AND status='draft'`,
		d.Goal, raw(d.Constraints), raw(d.ExpectedOutputs), raw(d.Acceptance), raw(d.Decisions),
		raw(d.SourceMessageIDs), d.ContextVersion, now(), d.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("goal draft is not editable")
	}
	return nil
}

func (s *Store) ConfirmGoalDraft(id, objectiveID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`UPDATE goal_drafts SET status='confirmed', updated_at=? WHERE id=? AND status='draft'`,
		now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("goal draft is not editable")
	}
	_, err = s.db.Exec(`UPDATE conversations SET objective_id=?, updated_at=? WHERE id=
		(SELECT conversation_id FROM goal_drafts WHERE id=?)`, objectiveID, now(), id)
	return err
}

func (s *Store) SetObjective(conversationID, objectiveID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE conversations SET objective_id=?, updated_at=? WHERE id=?`,
		objectiveID, now(), conversationID)
	return err
}

// PruneMessages keeps the newest N messages per conversation. Messages are the
// durable history; the cap prevents a single very long chat from growing without
// bound. It is intentionally much larger than the event cap.
func (s *Store) PruneMessages(conversationID string, keep int) error {
	if keep <= 0 {
		keep = 1000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM messages WHERE conversation_id=? AND rowid NOT IN (
		SELECT rowid FROM messages WHERE conversation_id=? ORDER BY created_at DESC, rowid DESC LIMIT ?)`,
		conversationID, conversationID, keep)
	return err
}

// PruneEvents bounds the persistent event log. Messages are the durable history;
// events are for reconnect replay and tool/permission cards.
func (s *Store) PruneEvents(keep int) error {
	if keep <= 0 {
		keep = 500
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`DELETE FROM events WHERE seq NOT IN (
		SELECT seq FROM events e2 WHERE e2.conversation_id = events.conversation_id
		ORDER BY seq DESC LIMIT ?)`, keep)
	return err
}
