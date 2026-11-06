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
	LastError   string    `json:"lastError,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	Role           string    `json:"role"` // user | assistant
	Text           string    `json:"text"`
	CreatedAt      time.Time `json:"createdAt"`
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
	last_error   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
	id              TEXT PRIMARY KEY,
	conversation_id TEXT NOT NULL,
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

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
	err := s.db.QueryRow(`SELECT id,title,project_id,objective_id,dir,session_id,model,status,last_error,created_at,updated_at
		FROM conversations WHERE id=?`, id).Scan(
		&c.ID, &c.Title, &c.ProjectID, &c.ObjectiveID, &c.Dir, &c.SessionID, &c.Model,
		&c.Status, &c.LastError, &created, &updated)
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
		c.status,c.last_error,c.created_at,c.updated_at,
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
		if err := rows.Scan(&c.ID, &c.Title, &c.ProjectID, &c.ObjectiveID, &c.Dir, &c.SessionID, &c.Model,
			&c.Status, &c.LastError, &created, &updated, &c.LastMessage); err != nil {
			return nil, err
		}
		c.CreatedAt, c.UpdatedAt = parseTime(created), parseTime(updated)
		out = append(out, c)
	}
	return out, rows.Err()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := Message{ID: fmt.Sprintf("msg-%d", time.Now().UnixNano()), ConversationID: conversationID,
		Role: role, Text: text, CreatedAt: time.Now().UTC()}
	_, err := s.db.Exec(`INSERT INTO messages (id,conversation_id,role,text,created_at) VALUES (?,?,?,?,?)`,
		msg.ID, msg.ConversationID, msg.Role, msg.Text, msg.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return Message{}, err
	}
	_, _ = s.db.Exec(`UPDATE conversations SET updated_at=? WHERE id=?`, now(), conversationID)
	return msg, nil
}

func (s *Store) Messages(conversationID string, limit int, before string) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	query := `SELECT id,conversation_id,role,text,created_at FROM messages
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
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Text, &created); err != nil {
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
