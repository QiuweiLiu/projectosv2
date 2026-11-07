// Package store persists Kernel state in SQLite. It is the only writer of Task
// and Run status.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"projectos/kernel/internal/domain"
)

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	id            TEXT PRIMARY KEY,
	objective     TEXT NOT NULL,
	prompt        TEXT NOT NULL,
	validator     TEXT NOT NULL,
	artifact_path TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL,
	attempts      INTEGER NOT NULL DEFAULT 0,
	max_attempts  INTEGER NOT NULL DEFAULT 1,
	last_error    TEXT NOT NULL DEFAULT '',
	objective_id  TEXT NOT NULL DEFAULT '',
	plan_version  INTEGER NOT NULL DEFAULT 0,
	commands      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS task_deps (
	task_id    TEXT NOT NULL,
	depends_on TEXT NOT NULL,
	PRIMARY KEY (task_id, depends_on)
);
CREATE TABLE IF NOT EXISTS runs (
	id          TEXT PRIMARY KEY,
	task_id     TEXT NOT NULL,
	attempt     INTEGER NOT NULL,
	backend     TEXT NOT NULL,
	status      TEXT NOT NULL,
	result      TEXT NOT NULL DEFAULT '',
	evidence    TEXT NOT NULL DEFAULT '',
	external_id  TEXT NOT NULL DEFAULT '',
	prompt_chars  INTEGER NOT NULL DEFAULT 0,
	output_chars  INTEGER NOT NULL DEFAULT 0,
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	usage_estimated INTEGER NOT NULL DEFAULT 0,
	session_id   TEXT NOT NULL DEFAULT '',
	started_at   TEXT NOT NULL,
	finished_at  TEXT
);
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS evidence (
	id               TEXT PRIMARY KEY,
	run_id           TEXT NOT NULL,
	task_id          TEXT NOT NULL,
	artifact_path    TEXT NOT NULL DEFAULT '',
	artifact_version TEXT NOT NULL DEFAULT '',
	digest           TEXT NOT NULL DEFAULT '',
	accepted         INTEGER NOT NULL,
	reason           TEXT NOT NULL DEFAULT '',
	at               TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS reviews (
	id               TEXT PRIMARY KEY,
	task_id          TEXT NOT NULL,
	artifact_version TEXT NOT NULL,
	kind             TEXT NOT NULL,
	plan_version     INTEGER NOT NULL DEFAULT 0,
	findings         TEXT NOT NULL,
	blocking         INTEGER NOT NULL,
	prompt_chars     INTEGER NOT NULL DEFAULT 0,
	output_chars     INTEGER NOT NULL DEFAULT 0,
	prompt_tokens    INTEGER NOT NULL DEFAULT 0,
	output_tokens    INTEGER NOT NULL DEFAULT 0,
	usage_estimated  INTEGER NOT NULL DEFAULT 0,
	at               TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_reviews_version_kind
	ON reviews (task_id, artifact_version, kind);
CREATE TABLE IF NOT EXISTS events (
	seq     INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id TEXT NOT NULL DEFAULT '',
	kind    TEXT NOT NULL,
	detail  TEXT NOT NULL DEFAULT '',
	at      TEXT NOT NULL
);
`

type Store struct {
	db   *sql.DB
	lock *Lock
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("store path must not be empty")
	}
	// Opening for writing also takes ownership of the state dir. Recovery below
	// rewrites every running Run to unknown, so it must never happen while a
	// scheduler is live: the lock is what makes that impossible instead of a
	// convention. Read-side commands use OpenReadOnly, which skips recovery.
	lock, err := AcquireLock(filepath.Join(filepath.Dir(path), "kernel.lock"))
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		lock.Release()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		lock.Release()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if _, err := db.Exec(goalSchema); err != nil {
		db.Close()
		lock.Release()
		return nil, fmt.Errorf("goal schema: %w", err)
	}
	s := &Store{db: db, lock: lock}
	if err := s.migrate(); err != nil {
		db.Close()
		lock.Release()
		return nil, err
	}
	if err := s.recoverInterrupted(); err != nil {
		db.Close()
		lock.Release()
		return nil, err
	}
	return s, nil
}

// OpenReadOnly opens an existing state database without schema creation or
// crash recovery, so `kernel status` cannot change what it reports.
func OpenReadOnly(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("store path must not be empty")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("state database %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	err := s.db.Close()
	if s.lock != nil {
		_ = s.lock.Release()
		s.lock = nil
	}
	return err
}

// migrate adds columns that older state databases predate. CREATE TABLE IF NOT
// EXISTS does not alter existing tables, so a long-lived state dir needs this.
func (s *Store) migrate() error {
	columns := map[string]string{
		"prompt_chars":    "ALTER TABLE runs ADD COLUMN prompt_chars INTEGER NOT NULL DEFAULT 0",
		"output_chars":    "ALTER TABLE runs ADD COLUMN output_chars INTEGER NOT NULL DEFAULT 0",
		"prompt_tokens":   "ALTER TABLE runs ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0",
		"output_tokens":   "ALTER TABLE runs ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0",
		"usage_estimated": "ALTER TABLE runs ADD COLUMN usage_estimated INTEGER NOT NULL DEFAULT 0",
		"session_id":      "ALTER TABLE runs ADD COLUMN session_id TEXT NOT NULL DEFAULT ''",
	}
	for name, ddl := range map[string]string{
		"objective_id": "ALTER TABLE tasks ADD COLUMN objective_id TEXT NOT NULL DEFAULT ''",
		"plan_version": "ALTER TABLE tasks ADD COLUMN plan_version INTEGER NOT NULL DEFAULT 0",
		"commands":     "ALTER TABLE tasks ADD COLUMN commands TEXT NOT NULL DEFAULT ''",
	} {
		if ok, err := s.hasColumn("tasks", name); err == nil && !ok {
			if _, err := s.db.Exec(ddl); err != nil {
				return fmt.Errorf("migrate tasks.%s: %w", name, err)
			}
		}
	}
	for name, ddl := range map[string]string{
		"plan_version":    "ALTER TABLE reviews ADD COLUMN plan_version INTEGER NOT NULL DEFAULT 0",
		"prompt_chars":    "ALTER TABLE reviews ADD COLUMN prompt_chars INTEGER NOT NULL DEFAULT 0",
		"output_chars":    "ALTER TABLE reviews ADD COLUMN output_chars INTEGER NOT NULL DEFAULT 0",
		"prompt_tokens":   "ALTER TABLE reviews ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0",
		"output_tokens":   "ALTER TABLE reviews ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0",
		"usage_estimated": "ALTER TABLE reviews ADD COLUMN usage_estimated INTEGER NOT NULL DEFAULT 0",
	} {
		if ok, err := s.hasColumn("reviews", name); err == nil && !ok {
			if _, err := s.db.Exec(ddl); err != nil {
				return fmt.Errorf("migrate reviews.%s: %w", name, err)
			}
		}
	}
	have := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(runs)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	for name, ddl := range columns {
		if !have[name] {
			if _, err := s.db.Exec(ddl); err != nil {
				return fmt.Errorf("migrate %s: %w", name, err)
			}
		}
	}
	for name, ddl := range map[string]string{
		"prompt_chars":    "ALTER TABLE plans ADD COLUMN prompt_chars INTEGER NOT NULL DEFAULT 0",
		"output_chars":    "ALTER TABLE plans ADD COLUMN output_chars INTEGER NOT NULL DEFAULT 0",
		"prompt_tokens":   "ALTER TABLE plans ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0",
		"output_tokens":   "ALTER TABLE plans ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0",
		"usage_estimated": "ALTER TABLE plans ADD COLUMN usage_estimated INTEGER NOT NULL DEFAULT 0",
	} {
		if ok, err := s.hasColumn("plans", name); err == nil && !ok {
			if _, err := s.db.Exec(ddl); err != nil {
				return fmt.Errorf("migrate plans.%s: %w", name, err)
			}
		}
	}
	return nil
}

// recoverInterrupted never guesses an outcome: runs that were in flight when the
// process died become unknown, and their tasks are blocked for explicit requeue.
func (s *Store) recoverInterrupted() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE runs SET status='unknown', finished_at=? WHERE status='running'`,
		now()); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE tasks SET status='blocked', last_error='interrupted: outcome unknown'
		WHERE status='running'`); err != nil {
		return err
	}
	return tx.Commit()
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Store) CreateTask(t domain.Task) error {
	if strings.TrimSpace(string(t.ID)) == "" {
		return fmt.Errorf("task id must not be empty")
	}
	if strings.TrimSpace(t.Validator) == "" {
		return fmt.Errorf("task %s has no validator", t.ID)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if t.MaxAttempts <= 0 {
		t.MaxAttempts = 1
	}
	cmds, err := json.Marshal(t.Commands)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO tasks (id, objective, prompt, validator, artifact_path, status, attempts, max_attempts, objective_id, plan_version, commands)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(t.ID), t.Objective, t.Prompt, t.Validator, t.ArtifactPath, string(domain.TaskPending), t.Attempts, t.MaxAttempts,
		t.ObjectiveID, t.PlanVersion, string(cmds)); err != nil {
		return fmt.Errorf("insert task %s: %w", t.ID, err)
	}
	for _, dep := range t.Deps {
		if _, err := tx.Exec(`INSERT INTO task_deps (task_id, depends_on) VALUES (?, ?)`, string(t.ID), string(dep)); err != nil {
			return fmt.Errorf("insert dep %s->%s: %w", t.ID, dep, err)
		}
	}
	return tx.Commit()
}

func (s *Store) taskFromRow(row *sql.Row) (domain.Task, bool, error) {
	var t domain.Task
	var cmds string
	err := row.Scan(&t.ID, &t.Objective, &t.Prompt, &t.Validator, &t.ArtifactPath, &t.Status, &t.Attempts, &t.MaxAttempts, &t.LastError, &t.ObjectiveID, &t.PlanVersion, &cmds)
	if err == sql.ErrNoRows {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	deps, err := s.deps(t.ID)
	if err != nil {
		return t, false, err
	}
	if cmds != "" {
		if err := json.Unmarshal([]byte(cmds), &t.Commands); err != nil {
			return t, false, fmt.Errorf("task %s commands: %w", t.ID, err)
		}
	}
	t.Deps = deps
	return t, true, nil
}

func (s *Store) deps(id domain.TaskID) ([]domain.TaskID, error) {
	rows, err := s.db.Query(`SELECT depends_on FROM task_deps WHERE task_id=? ORDER BY depends_on`, string(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskID
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, domain.TaskID(d))
	}
	return out, rows.Err()
}

func (s *Store) Get(id domain.TaskID) (domain.Task, bool, error) {
	return s.taskFromRow(s.db.QueryRow(`SELECT id, objective, prompt, validator, artifact_path, status, attempts, max_attempts, last_error, objective_id, plan_version, commands
		FROM tasks WHERE id=?`, string(id)))
}

func (s *Store) List() ([]domain.Task, error) {
	rows, err := s.db.Query(`SELECT id, objective, prompt, validator, artifact_path, status, attempts, max_attempts, last_error, objective_id, plan_version, commands
		FROM tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		var t domain.Task
		var cmds string
		if err := rows.Scan(&t.ID, &t.Objective, &t.Prompt, &t.Validator, &t.ArtifactPath, &t.Status, &t.Attempts, &t.MaxAttempts, &t.LastError, &t.ObjectiveID, &t.PlanVersion, &cmds); err != nil {
			return nil, err
		}
		if cmds != "" {
			if err := json.Unmarshal([]byte(cmds), &t.Commands); err != nil {
				return nil, fmt.Errorf("task %s commands: %w", t.ID, err)
			}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Deps, err = s.deps(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Ready returns pending tasks whose every dependency is accepted.
func (s *Store) Ready() ([]domain.Task, error) {
	tasks, err := s.List()
	if err != nil {
		return nil, err
	}
	accepted := map[domain.TaskID]bool{}
	for _, t := range tasks {
		if t.Status == domain.TaskAccepted {
			accepted[t.ID] = true
		}
	}
	var ready []domain.Task
	for _, t := range tasks {
		if t.Status != domain.TaskPending {
			continue
		}
		ok := true
		for _, d := range t.Deps {
			if !accepted[d] {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, t)
		}
	}
	return ready, nil
}

// Claim atomically moves one pending task to running and records a new attempt.
func (s *Store) Claim(id domain.TaskID, runID, backend string) (domain.Run, bool, error) {
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(runID) == "" {
		return domain.Run{}, false, fmt.Errorf("claim needs a task id and a run id")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return domain.Run{}, false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE tasks SET status='running', attempts=attempts+1
		WHERE id=? AND status='pending'`, string(id))
	if err != nil {
		return domain.Run{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return domain.Run{}, false, err
	}
	if n != 1 {
		return domain.Run{}, false, nil
	}
	var attempt int
	if err := tx.QueryRow(`SELECT attempts FROM tasks WHERE id=?`, string(id)).Scan(&attempt); err != nil {
		return domain.Run{}, false, err
	}
	run := domain.Run{ID: runID, TaskID: id, Attempt: attempt, Backend: backend, Status: domain.RunRunning}
	if _, err := tx.Exec(`INSERT INTO runs (id, task_id, attempt, backend, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`, runID, string(id), attempt, backend, string(domain.RunRunning), now()); err != nil {
		return domain.Run{}, false, err
	}
	return run, true, tx.Commit()
}

func (s *Store) ActiveRuns() ([]domain.Run, error) {
	rows, err := s.db.Query(`SELECT id, task_id, attempt, backend, status, result, evidence, external_id, started_at
		FROM runs WHERE status='running' ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		var r domain.Run
		var started string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Attempt, &r.Backend, &r.Status, &r.Result, &r.Evidence, &r.ExternalID, &started); err != nil {
			return nil, err
		}
		var perr error
		if r.StartedAt, perr = time.Parse(time.RFC3339Nano, started); perr != nil {
			return nil, fmt.Errorf("run %s: bad started_at %q: %w", r.ID, started, perr)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ErrStaleRun means the run is no longer the active attempt for its task, so a
// late result must not change task state.
var ErrStaleRun = errors.New("stale run: not the active attempt for this task")

// Settlement is one atomic transition: run result + task outcome + evidence.
type Settlement struct {
	RunID          string
	RunStatus      domain.RunStatus
	Result         string
	Note           string
	Accepted       bool
	AllowRetry     bool
	PromptChars    int
	OutputChars    int
	PromptTokens   int
	OutputTokens   int
	UsageEstimated bool
	SessionID      string
	Evidence       domain.Evidence
}

// SettleRun applies a run result exactly once. It verifies that the run is still
// running and that the task is still on that attempt; a late or duplicated result
// returns ErrStaleRun instead of rewriting the task.
func (s *Store) SettleRun(x Settlement) (domain.TaskStatus, error) {
	if strings.TrimSpace(x.RunID) == "" {
		return "", fmt.Errorf("settle needs a run id")
	}
	switch x.RunStatus {
	case domain.RunSucceeded, domain.RunFailed, domain.RunCancelled, domain.RunUnknown:
	default:
		return "", fmt.Errorf("settle: invalid run status %q", x.RunStatus)
	}
	if x.Accepted && x.RunStatus != domain.RunSucceeded {
		return "", fmt.Errorf("settle: a run can only be accepted after it succeeded")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var taskID string
	var attempt int
	if err := tx.QueryRow(`SELECT task_id, attempt FROM runs WHERE id=? AND status='running'`,
		x.RunID).Scan(&taskID, &attempt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrStaleRun
		}
		return "", err
	}
	res, err := tx.Exec(`UPDATE runs SET status=?, result=?, evidence=?, prompt_chars=?, output_chars=?,
		prompt_tokens=?, output_tokens=?, usage_estimated=?, session_id=?, finished_at=?
		WHERE id=? AND status='running'`,
		string(x.RunStatus), x.Result, x.Note, x.PromptChars, x.OutputChars,
		x.PromptTokens, x.OutputTokens, boolInt(x.UsageEstimated), x.SessionID, now(), x.RunID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrStaleRun
	}

	// The task may have been retried while this result was in flight. Binding the
	// update to the run's attempt is what stops a stale result from overwriting a
	// newer attempt.
	var taskAttempt int
	if err := tx.QueryRow(`SELECT attempts FROM tasks WHERE id=?`, taskID).Scan(&taskAttempt); err != nil {
		return "", err
	}
	if taskAttempt != attempt {
		return "", ErrStaleRun
	}

	var next domain.TaskStatus
	if x.Accepted {
		next = domain.TaskAccepted
	} else {
		var attempts, max int
		if err := tx.QueryRow(`SELECT attempts, max_attempts FROM tasks WHERE id=?`, taskID).
			Scan(&attempts, &max); err != nil {
			return "", err
		}
		next = domain.TaskBlocked
		if x.AllowRetry && attempts < max {
			next = domain.TaskPending
		}
	}
	res, err = tx.Exec(`UPDATE tasks SET status=?, last_error=? WHERE id=? AND status='running' AND attempts=?`,
		string(next), x.Note, taskID, attempt)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrStaleRun
	}

	sum := sha256.Sum256([]byte(x.Result))
	ev := x.Evidence
	ev.RunID = x.RunID
	ev.TaskID = domain.TaskID(taskID)
	ev.Digest = hex.EncodeToString(sum[:])
	ev.Accepted = x.Accepted
	ev.Reason = x.Note
	accepted := 0
	if ev.Accepted {
		accepted = 1
	}
	if _, err := tx.Exec(`INSERT INTO evidence
		(id, run_id, task_id, artifact_path, artifact_version, digest, accepted, reason, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fmt.Sprintf("%s-%d", x.RunID, time.Now().UnixNano()), ev.RunID, string(ev.TaskID),
		ev.ArtifactPath, ev.ArtifactVersion, ev.Digest, accepted, ev.Reason, now()); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return next, nil
}

// SetExternal records the backend's own identifier for a run.
func (s *Store) SetExternal(runID, externalID string) error {
	_, err := s.db.Exec(`UPDATE runs SET external_id=? WHERE id=?`, externalID, runID)
	return err
}

// AcceptedResult returns the artifact of the latest successful attempt.
func (s *Store) AcceptedResult(id domain.TaskID) (string, error) {
	var result string
	err := s.db.QueryRow(`SELECT result FROM runs WHERE task_id=? AND status='succeeded'
		ORDER BY attempt DESC LIMIT 1`, string(id)).Scan(&result)
	return result, err
}

// Reject records a failed attempt and either requeues the task or blocks it.

// BlockPending marks every still-pending task. Used when a budget stops the run.
func (s *Store) BlockPending(reason string) ([]domain.TaskID, error) {
	rows, err := s.db.Query(`SELECT id FROM tasks WHERE status='pending' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var ids []domain.TaskID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, domain.TaskID(id))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE tasks SET status='blocked', last_error=? WHERE id=? AND status='pending'`,
			reason, string(id)); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// BlockDependents marks pending tasks whose dependency can never succeed. A
// dependent of a blocked task is itself blocked: it is never silently started.
func (s *Store) BlockDependents(reason string) ([]domain.TaskID, error) {
	rows, err := s.db.Query(`SELECT id FROM tasks t WHERE status='pending' AND EXISTS (
		SELECT 1 FROM task_deps d JOIN tasks dep ON dep.id = d.depends_on
		WHERE d.task_id = t.id AND dep.status = 'blocked')`)
	if err != nil {
		return nil, err
	}
	var ids []domain.TaskID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, domain.TaskID(id))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE tasks SET status='blocked', last_error=? WHERE id=? AND status='pending'`,
			reason, string(id)); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// RecordEvidence stores the artifact version behind one acceptance decision.
func (s *Store) RecordEvidence(ev domain.Evidence) error {
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("%s-%d", ev.RunID, time.Now().UnixNano())
	}
	accepted := 0
	if ev.Accepted {
		accepted = 1
	}
	_, err := s.db.Exec(`INSERT INTO evidence
		(id, run_id, task_id, artifact_path, artifact_version, digest, accepted, reason, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.RunID, string(ev.TaskID), ev.ArtifactPath, ev.ArtifactVersion, ev.Digest, accepted, ev.Reason, now())
	return err
}

func (s *Store) EvidenceFor(taskID domain.TaskID) ([]domain.Evidence, error) {
	rows, err := s.db.Query(`SELECT id, run_id, task_id, artifact_path, artifact_version, digest, accepted, reason, at
		FROM evidence WHERE task_id=? ORDER BY at`, string(taskID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Evidence
	for rows.Next() {
		var ev domain.Evidence
		var accepted int
		var at string
		if err := rows.Scan(&ev.ID, &ev.RunID, &ev.TaskID, &ev.ArtifactPath, &ev.ArtifactVersion,
			&ev.Digest, &accepted, &ev.Reason, &at); err != nil {
			return nil, err
		}
		ev.Accepted = accepted == 1
		if ev.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("evidence %s: bad timestamp %q: %w", ev.ID, at, err)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

var (
	ErrReviewStale = errors.New("review targets a stale artifact version")
	ErrReviewLimit = errors.New("review budget for this artifact version is exhausted")
	ErrReviewScope = errors.New("targeted review covers findings outside the blocking set")
)

// LastBlockingFindings returns the P0/P1 findings from the most recent full review.
func (s *Store) LastBlockingFindings(taskID domain.TaskID) ([]domain.Finding, error) {
	var raw string
	err := s.db.QueryRow(`SELECT findings FROM reviews WHERE task_id=? AND blocking=1
		ORDER BY at DESC LIMIT 1`, string(taskID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var findings []domain.Finding
	if err := json.Unmarshal([]byte(raw), &findings); err != nil {
		return nil, err
	}
	var out []domain.Finding
	for _, f := range findings {
		if f.Severity == domain.SeverityP0 || f.Severity == domain.SeverityP1 {
			out = append(out, f)
		}
	}
	return out, nil
}

// LastReviewFindings returns every finding from the most recent full review,
// blocking or not, so a retry can be told what to fix and what to improve.
func (s *Store) LastReviewFindings(taskID domain.TaskID) ([]domain.Finding, error) {
	var raw string
	err := s.db.QueryRow(`SELECT findings FROM reviews WHERE task_id=? AND blocking=1
		ORDER BY at DESC LIMIT 1`, string(taskID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var findings []domain.Finding
	if err := json.Unmarshal([]byte(raw), &findings); err != nil {
		return nil, err
	}
	return findings, nil
}

// LastBlockingCodes returns the P0/P1 codes from the most recent full review.
func (s *Store) LastBlockingCodes(taskID domain.TaskID) ([]string, error) {
	codes, err := s.blockingCodes(taskID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(codes))
	for code := range codes {
		out = append(out, code)
	}
	return out, nil
}

// blockingCodes returns the codes of P0/P1 findings in the most recent full review.
func (s *Store) blockingCodes(taskID domain.TaskID) (map[string]bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT findings FROM reviews WHERE task_id=? AND blocking=1
		ORDER BY at DESC LIMIT 1`, string(taskID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var findings []domain.Finding
	if err := json.Unmarshal([]byte(raw), &findings); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range findings {
		if f.Severity == domain.SeverityP0 || f.Severity == domain.SeverityP1 {
			out[f.Code] = true
		}
	}
	return out, nil
}

// AddReview enforces the bounded-review policy: one full review per artifact
// version, plus at most one targeted re-review of the same version.
func (s *Store) AddReview(rev domain.Review) error {
	if strings.TrimSpace(string(rev.TaskID)) == "" {
		return fmt.Errorf("review needs a task id")
	}
	if rev.ArtifactVersion == "" {
		return ErrReviewStale
	}
	var full, targeted, acceptance int
	if err := s.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN kind='full' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN kind='targeted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN kind='acceptance' THEN 1 ELSE 0 END),0)
		FROM reviews WHERE task_id=? AND artifact_version=?`,
		string(rev.TaskID), rev.ArtifactVersion).Scan(&full, &targeted, &acceptance); err != nil {
		return err
	}
	switch rev.Kind {
	case domain.ReviewFull:
		if full > 0 {
			return ErrReviewLimit
		}
	case domain.ReviewTargeted:
		var priorFull int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM reviews WHERE task_id=? AND kind='full'`,
			string(rev.TaskID)).Scan(&priorFull); err != nil {
			return err
		}
		if priorFull == 0 {
			return fmt.Errorf("targeted review requires a prior full review of this task")
		}
		if targeted > 0 {
			return ErrReviewLimit
		}
		// Targeted scope is enforced by severity in the Engine: a targeted review
		// may only report blocking findings, so a new code for the same defect is
		// still in scope.
		if len(rev.Findings) > 0 {
			for _, f := range rev.Findings {
				if f.Severity != domain.SeverityP0 && f.Severity != domain.SeverityP1 {
					return fmt.Errorf("%w: targeted review may only report P0/P1", ErrReviewScope)
				}
			}
		}
	case domain.ReviewAcceptance:
		if acceptance > 0 {
			return ErrReviewLimit
		}
	case domain.ReviewObjective:
		var objective int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM reviews
			WHERE task_id=? AND artifact_version=? AND kind='objective'`,
			string(rev.TaskID), rev.ArtifactVersion).Scan(&objective); err != nil {
			return err
		}
		if objective > 0 {
			return ErrReviewLimit
		}
	default:
		return fmt.Errorf("unknown review kind %q", rev.Kind)
	}
	raw, err := json.Marshal(rev.Findings)
	if err != nil {
		return err
	}
	blocking := 0
	if rev.Blocking() {
		blocking = 1
	}
	_, err = s.db.Exec(`INSERT INTO reviews
		(id, task_id, artifact_version, kind, plan_version, findings, blocking, prompt_chars, output_chars,
		 prompt_tokens, output_tokens, usage_estimated, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fmt.Sprintf("%s-%s-%d", rev.TaskID, rev.ArtifactVersion, time.Now().UnixNano()),
		string(rev.TaskID), rev.ArtifactVersion, string(rev.Kind), rev.PlanVersion, string(raw), blocking,
		rev.PromptChars, rev.OutputChars, rev.PromptTokens, rev.OutputTokens,
		boolInt(rev.UsageEstimated), now())
	return err
}

// ReviewVerdict reports the most recent judgment of one kind for an artifact
// version. Unlike HasBlockingReview it never mixes kinds: an objective verdict
// cannot be overwritten by a targeted task review.
func (s *Store) ReviewVerdict(taskID domain.TaskID, artifactVersion string, kind domain.ReviewKind) (blocking bool, found bool, err error) {
	var value int
	err = s.db.QueryRow(`SELECT blocking FROM reviews
		WHERE task_id=? AND artifact_version=? AND kind=?
		ORDER BY at DESC, rowid DESC LIMIT 1`,
		string(taskID), artifactVersion, string(kind)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return value == 1, true, nil
}

// HasBlockingReview reports whether the LATEST review of this version blocks.
// A clean targeted re-review resolves an earlier blocking full review of the
// same artifact state; counting old rows would make that fix invisible.
// AcceptanceVerdict reports the most recent acceptance judgment for one artifact
// version. The reviewer's targeted re-review must not overwrite it.
func (s *Store) AcceptanceVerdict(taskID domain.TaskID, artifactVersion string) (blocking bool, found bool, err error) {
	var value int
	err = s.db.QueryRow(`SELECT blocking FROM reviews
		WHERE task_id=? AND artifact_version=? AND kind='acceptance'
		ORDER BY at DESC, rowid DESC LIMIT 1`,
		string(taskID), artifactVersion).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return value == 1, true, nil
}

func (s *Store) HasBlockingReview(taskID domain.TaskID, artifactVersion string) (bool, error) {
	var blocking int
	err := s.db.QueryRow(`SELECT blocking FROM reviews
		WHERE task_id=? AND artifact_version=?
		ORDER BY at DESC, rowid DESC LIMIT 1`, string(taskID), artifactVersion).Scan(&blocking)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return blocking == 1, nil
}

func (s *Store) ReviewsFor(taskID domain.TaskID) ([]domain.Review, error) {
	withUsage, err := s.hasColumn("reviews", "prompt_chars")
	if err != nil {
		return nil, err
	}
	withTokens, err := s.hasColumn("reviews", "prompt_tokens")
	if err != nil {
		return nil, err
	}
	withPlan, err := s.hasColumn("reviews", "plan_version")
	if err != nil {
		return nil, err
	}
	columns := "id, task_id, artifact_version, kind, findings, at"
	if withUsage {
		columns += ", prompt_chars, output_chars"
	}
	if withTokens {
		columns += ", prompt_tokens, output_tokens, usage_estimated"
	}
	if withPlan {
		columns += ", plan_version"
	}
	rows, err := s.db.Query("SELECT "+columns+" FROM reviews WHERE task_id=? ORDER BY at", string(taskID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Review
	for rows.Next() {
		var rev domain.Review
		var findings, at string
		var estimated int
		dest := []any{&rev.ID, &rev.TaskID, &rev.ArtifactVersion, &rev.Kind, &findings, &at}
		if withUsage {
			dest = append(dest, &rev.PromptChars, &rev.OutputChars)
		}
		if withTokens {
			dest = append(dest, &rev.PromptTokens, &rev.OutputTokens, &estimated)
		}
		if withPlan {
			dest = append(dest, &rev.PlanVersion)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		rev.UsageEstimated = estimated == 1
		_ = json.Unmarshal([]byte(findings), &rev.Findings)
		if rev.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("review %s: bad timestamp %q: %w", rev.ID, at, err)
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

func (s *Store) SetMeta(key, value string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("meta key must not be empty")
	}
	_, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) GetMeta(key string) (string, error) {
	ok, err := s.hasTable("meta")
	if err != nil || !ok {
		return "", err // an old read-only state dir simply has no metadata
	}
	var value string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

func (s *Store) hasTable(name string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// UsageChars reports the total prompt/output characters recorded so far, used
// for a character-based token estimate.
func (s *Store) UsageChars() (prompt, output int, err error) {
	// A read-only connection to an older state dir may predate these columns on
	// either table; each table degrades to zero independently.
	runsOK, err := s.hasColumn("runs", "prompt_chars")
	if err != nil {
		return 0, 0, err
	}
	reviewsOK, err := s.hasColumn("reviews", "prompt_chars")
	if err != nil {
		return 0, 0, err
	}
	if runsOK {
		if err := s.db.QueryRow(`SELECT COALESCE(SUM(prompt_chars),0), COALESCE(SUM(output_chars),0) FROM runs`).
			Scan(&prompt, &output); err != nil {
			return 0, 0, err
		}
	}
	if reviewsOK {
		var rp, ro int
		if err := s.db.QueryRow(`SELECT COALESCE(SUM(prompt_chars),0), COALESCE(SUM(output_chars),0) FROM reviews`).
			Scan(&rp, &ro); err != nil {
			return 0, 0, err
		}
		prompt += rp
		output += ro
	}
	plansOK, err := s.hasColumn("plans", "prompt_chars")
	if err != nil {
		return 0, 0, err
	}
	if plansOK {
		var pp, po int
		if err := s.db.QueryRow(`SELECT COALESCE(SUM(prompt_chars),0), COALESCE(SUM(output_chars),0) FROM plans`).
			Scan(&pp, &po); err != nil {
			return 0, 0, err
		}
		prompt += pp
		output += po
	}
	return prompt, output, nil
}

// LastSession returns the most recent session id for a task. A retry uses it to
// continue the same provider conversation instead of starting from zero.
func (s *Store) LastSession(taskID domain.TaskID, backend string) (string, error) {
	ok, err := s.hasColumn("runs", "session_id")
	if err != nil || !ok {
		return "", err
	}
	var id string
	err = s.db.QueryRow(`SELECT session_id FROM runs
		WHERE task_id=? AND backend=? AND session_id<>''
		ORDER BY attempt DESC, started_at DESC LIMIT 1`,
		string(taskID), backend).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// UsageTokens reports provider tokens when available and falls back to the
// character estimate row by row. The boolean says whether any part of the total
// is estimated, so the cockpit can label it instead of implying precision.
func (s *Store) UsageTokens() (prompt, output int, estimated bool, err error) {
	sum := func(table string) (int, int, bool, error) {
		hasTokens, err := s.hasColumn(table, "prompt_tokens")
		if err != nil {
			return 0, 0, false, err
		}
		hasChars, err := s.hasColumn(table, "prompt_chars")
		if err != nil {
			return 0, 0, false, err
		}
		if !hasChars && !hasTokens {
			return 0, 0, false, nil
		}
		query := `SELECT 0,0,0`
		if hasTokens {
			query = `SELECT
				COALESCE(SUM(CASE WHEN prompt_tokens > 0 THEN prompt_tokens ELSE prompt_chars/4 END),0),
				COALESCE(SUM(CASE WHEN output_tokens > 0 THEN output_tokens ELSE output_chars/4 END),0),
				COALESCE(MAX(CASE WHEN usage_estimated = 1 OR prompt_tokens = 0 THEN 1 ELSE 0 END),0)
				FROM ` + table
		} else {
			query = `SELECT COALESCE(SUM(prompt_chars/4),0), COALESCE(SUM(output_chars/4),0), 1 FROM ` + table
		}
		var p, o, e int
		if err := s.db.QueryRow(query).Scan(&p, &o, &e); err != nil {
			return 0, 0, false, err
		}
		return p, o, e == 1, nil
	}
	rp, ro, re, err := sum("runs")
	if err != nil {
		return 0, 0, false, err
	}
	vrp, vro, vre, err := sum("reviews")
	if err != nil {
		return 0, 0, false, err
	}
	pp, po, pe, err := sum("plans")
	if err != nil {
		return 0, 0, false, err
	}
	return rp + vrp + pp, ro + vro + po, re || vre || pe, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// AbandonRunning closes the books after an objective-level abort: in-flight runs
// become unknown and their tasks blocked, so status never shows a ghost "running".
func (s *Store) AbandonRunning(reason string, details map[domain.TaskID]string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE runs SET status='unknown', evidence=?, finished_at=?
		WHERE status='running'`, reason, now())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.Exec(`UPDATE tasks SET status='blocked', last_error=? WHERE status='running'`, reason); err != nil {
		return 0, err
	}
	for taskID, detail := range details {
		if _, err := tx.Exec(`UPDATE tasks SET last_error=? WHERE id=? AND status='blocked'`,
			reason+"; "+detail, string(taskID)); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// CountReviews reports how many reviewer passes have been recorded; the count is
// durable so a restart cannot reset the review budget.
func (s *Store) CountReviews() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM reviews`).Scan(&n)
	return n, err
}

// RunCount reports how many runs have ever been started; used for budget caps.
func (s *Store) RunCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n)
	return n, err
}

func (s *Store) AppendEvent(taskID domain.TaskID, kind, detail string) error {

	_, err := s.db.Exec(`INSERT INTO events (task_id, kind, detail, at) VALUES (?, ?, ?, ?)`,
		string(taskID), kind, detail, now())
	return err
}

// EventsPage returns events after a sequence, oldest first, bounded by limit.
func (s *Store) EventsPage(after int64, limit int) ([]domain.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT seq, task_id, kind, detail, at FROM events
		WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var at string
		if err := rows.Scan(&e.Seq, &e.TaskID, &e.Kind, &e.Detail, &at); err != nil {
			return nil, err
		}
		if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("event %d: bad timestamp: %w", e.Seq, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventsTail returns the most recent events in ascending order. The cockpit
// needs "latest N", not "oldest N", on first load.
func (s *Store) EventsTail(limit int) ([]domain.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT seq, task_id, kind, detail, at FROM events ORDER BY seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var at string
		if err := rows.Scan(&e.Seq, &e.TaskID, &e.Kind, &e.Detail, &at); err != nil {
			return nil, err
		}
		if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func (s *Store) Events() ([]domain.Event, error) {
	rows, err := s.db.Query(`SELECT seq, task_id, kind, detail, at FROM events ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var at string
		if err := rows.Scan(&e.Seq, &e.TaskID, &e.Kind, &e.Detail, &at); err != nil {
			return nil, err
		}
		if e.At, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return nil, fmt.Errorf("event %d: bad timestamp %q: %w", e.Seq, at, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RunsForTask returns every attempt for one task, oldest first. Progress and
// recovery use the persisted Run status instead of guessing from prose.
func (s *Store) RunsForTask(taskID domain.TaskID) ([]domain.Run, error) {
	rows, err := s.db.Query(`SELECT id, task_id, attempt, backend, status, result, evidence, external_id, started_at
		FROM runs WHERE task_id=? ORDER BY attempt, started_at`, string(taskID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		var r domain.Run
		var started string
		if err := rows.Scan(&r.ID, &r.TaskID, &r.Attempt, &r.Backend, &r.Status, &r.Result, &r.Evidence, &r.ExternalID, &started); err != nil {
			return nil, err
		}
		if r.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
			return nil, fmt.Errorf("run %s: bad started_at %q: %w", r.ID, started, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RequeueBlocked is the only Kernel-approved way to resume a blocked task. It
// never touches accepted, running or superseded tasks, and grows the attempt
// cap by exactly one so a recovery loop is bounded.
func (s *Store) RequeueBlocked(taskID domain.TaskID, reason string, attemptCap int) (bool, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return false, fmt.Errorf("requeue needs a task id")
	}
	if attemptCap <= 0 {
		return false, fmt.Errorf("requeue needs a positive attempt cap")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status string
	var max, attempts int
	if err := tx.QueryRow(`SELECT status, max_attempts, attempts FROM tasks WHERE id=?`, string(taskID)).
		Scan(&status, &max, &attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("unknown task %s", taskID)
		}
		return false, err
	}
	if status != string(domain.TaskBlocked) && status != string(domain.TaskRejected) {
		return false, nil
	}
	if attempts >= attemptCap {
		return false, nil
	}
	// Preserve a plan's larger attempt allowance; only grow the cap when the
	// current cap would immediately re-block the requeued task.
	newMax := max
	if newMax <= attempts {
		newMax = attempts + 1
	}
	if newMax > attemptCap {
		newMax = attemptCap
	}
	res, err := tx.Exec(`UPDATE tasks SET status=?, max_attempts=?,
		last_error=CASE WHEN last_error='' THEN ? ELSE last_error END
		WHERE id=? AND status IN (?, ?)`,
		string(domain.TaskPending), newMax, reason, string(taskID),
		string(domain.TaskBlocked), string(domain.TaskRejected))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return false, nil
	}
	if err := reopenDependencyBlocks(tx); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// reopenDependencyBlocks reverses BlockDependents after a recovery requeue. It
// only touches tasks whose sole reason was an upstream block; a task with an
// independent failure keeps its own blocked state.
func reopenDependencyBlocks(tx *sql.Tx) error {
	for round := 0; round < 8; round++ {
		rows, err := tx.Query(`SELECT id FROM tasks
			WHERE status IN ('blocked','rejected') AND last_error LIKE 'dependency blocked%' ORDER BY id`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		changed := 0
		for _, id := range ids {
			deps, err := tx.Query(`SELECT depends_on FROM task_deps WHERE task_id=?`, id)
			if err != nil {
				return err
			}
			ok := true
			for deps.Next() {
				var dep string
				if err := deps.Scan(&dep); err != nil {
					deps.Close()
					return err
				}
				var status string
				if err := tx.QueryRow(`SELECT status FROM tasks WHERE id=?`, dep).Scan(&status); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						ok = false
						continue
					}
					deps.Close()
					return err
				}
				if status == string(domain.TaskBlocked) || status == string(domain.TaskRejected) ||
					status == string(domain.TaskSuperseded) {
					ok = false
				}
			}
			deps.Close()
			if !ok {
				continue
			}
			res, err := tx.Exec(`UPDATE tasks SET status=?, last_error=''
				WHERE id=? AND status IN ('blocked','rejected') AND last_error LIKE 'dependency blocked%'`,
				string(domain.TaskPending), id)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			changed += int(n)
		}
		if changed == 0 {
			return nil
		}
	}
	return nil
}
