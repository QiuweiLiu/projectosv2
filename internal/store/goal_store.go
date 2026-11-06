package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/goal"
)

const goalSchema = `
CREATE TABLE IF NOT EXISTS objectives (
	id           TEXT PRIMARY KEY,
	project_path TEXT NOT NULL DEFAULT '',
	title        TEXT NOT NULL DEFAULT '',
	goal         TEXT NOT NULL,
	constraints  TEXT NOT NULL DEFAULT '',
	expected     TEXT NOT NULL DEFAULT '',
	acceptance   TEXT NOT NULL DEFAULT '',
	status       TEXT NOT NULL,
	plan_version INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS plans (
	id           TEXT PRIMARY KEY,
	objective_id TEXT NOT NULL,
	version      INTEGER NOT NULL,
	status       TEXT NOT NULL,
	spec         TEXT NOT NULL,
	hash         TEXT NOT NULL,
	prompt_chars INTEGER NOT NULL DEFAULT 0,
	output_chars INTEGER NOT NULL DEFAULT 0,
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	usage_estimated INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL,
	confirmed_at TEXT,
	UNIQUE (objective_id, version)
);
`

type PlanRecord struct {
	ID          string
	ObjectiveID string
	Version     int
	Status      string
	Spec        goal.Plan
	Hash        string
	CreatedAt   time.Time
	ConfirmedAt *time.Time
}

func (s *Store) CreateObjective(o goal.Objective) error {
	if err := o.Validate(); err != nil {
		return err
	}
	now := now()
	_, err := s.db.Exec(`INSERT INTO objectives
		(id, project_path, title, goal, constraints, expected, acceptance, status, plan_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.ProjectPath, o.Title, o.Goal, o.Constraints, o.ExpectedOutcome, o.Acceptance,
		string(o.Status), o.PlanVersion, now, now)
	return err
}

func (s *Store) GetObjective(id string) (goal.Objective, bool, error) {
	var o goal.Objective
	var status, created, updated string
	err := s.db.QueryRow(`SELECT id, project_path, title, goal, constraints, expected, acceptance, status, plan_version, created_at, updated_at
		FROM objectives WHERE id=?`, id).Scan(&o.ID, &o.ProjectPath, &o.Title, &o.Goal, &o.Constraints,
		&o.ExpectedOutcome, &o.Acceptance, &status, &o.PlanVersion, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	if err != nil {
		return o, false, err
	}
	o.Status = goal.Status(status)
	if o.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return o, false, err
	}
	if o.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return o, false, err
	}
	return o, true, nil
}

func (s *Store) ListObjectives() ([]goal.Objective, error) {
	rows, err := s.db.Query(`SELECT id FROM objectives ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]goal.Objective, 0, len(ids))
	for _, id := range ids {
		o, _, err := s.GetObjective(id)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// MoveObjective validates the state machine transition before writing it.
func (s *Store) MoveObjective(id string, next goal.Status) error {
	o, ok, err := s.GetObjective(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown objective %s", id)
	}
	if !o.Status.CanMoveTo(next) {
		return fmt.Errorf("objective %s cannot move %s -> %s", id, o.Status, next)
	}
	_, err = s.db.Exec(`UPDATE objectives SET status=?, updated_at=? WHERE id=?`, string(next), now(), id)
	return err
}

func (s *Store) BumpObjectivePlan(id string, version int) error {
	_, err := s.db.Exec(`UPDATE objectives SET plan_version=?, updated_at=? WHERE id=?`, version, now(), id)
	return err
}

func (s *Store) CreatePlan(objectiveID string, version int, spec goal.Plan, hash string) (string, error) {
	return s.CreatePlanWithUsage(objectiveID, version, spec, hash, PlanUsage{})
}

type PlanUsage struct {
	PromptChars    int
	OutputChars    int
	PromptTokens   int
	OutputTokens   int
	UsageEstimated bool
}

func (s *Store) CreatePlanWithUsage(objectiveID string, version int, spec goal.Plan, hash string, usage PlanUsage) (string, error) {
	id := fmt.Sprintf("%s-plan-v%d", objectiveID, version)
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`INSERT INTO plans (id, objective_id, version, status, spec, hash,
		prompt_chars, output_chars, prompt_tokens, output_tokens, usage_estimated, created_at)
		VALUES (?, ?, ?, 'draft', ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, objectiveID, version, string(raw), hash,
		usage.PromptChars, usage.OutputChars, usage.PromptTokens, usage.OutputTokens,
		boolInt(usage.UsageEstimated), now())
	if err != nil {
		return "", fmt.Errorf("create plan v%d: %w", version, err)
	}
	return id, nil
}

func (s *Store) confirmTx(objID string, version int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE plans SET status='confirmed', confirmed_at=? WHERE objective_id=? AND version=? AND status='draft'`,
		now(), objID, version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("plan v%d is not a draft", version)
	}
	if _, err := tx.Exec(`UPDATE plans SET status='superseded' WHERE objective_id=? AND version<>? AND status='confirmed'`,
		objID, version); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE objectives SET plan_version=?, status=?, updated_at=? WHERE id=?`,
		version, string(goal.StatusActive), now(), objID); err != nil {
		return err
	}
	return tx.Commit()
}

// ConfirmPlan activates one draft version and supersedes any other confirmed plan.
func (s *Store) ConfirmPlan(objectiveID string, version int) error {
	o, ok, err := s.GetObjective(objectiveID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown objective %s", objectiveID)
	}
	if !o.Status.CanMoveTo(goal.StatusActive) && o.Status != goal.StatusActive {
		return fmt.Errorf("objective %s cannot be activated from %s", objectiveID, o.Status)
	}
	return s.confirmTx(objectiveID, version)
}

func (s *Store) GetPlan(objectiveID string, version int) (PlanRecord, bool, error) {
	if ok, err := s.hasTable("plans"); err != nil {
		return PlanRecord{}, false, err
	} else if !ok {
		return PlanRecord{}, false, nil
	}
	var p PlanRecord
	var raw, created string
	var confirmed sql.NullString
	err := s.db.QueryRow(`SELECT id, objective_id, version, status, spec, hash, created_at, confirmed_at
		FROM plans WHERE objective_id=? AND version=?`, objectiveID, version).
		Scan(&p.ID, &p.ObjectiveID, &p.Version, &p.Status, &raw, &p.Hash, &created, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err := json.Unmarshal([]byte(raw), &p.Spec); err != nil {
		return p, false, fmt.Errorf("plan %s: %w", p.ID, err)
	}
	if p.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return p, false, err
	}
	if confirmed.Valid {
		at, err := time.Parse(time.RFC3339Nano, confirmed.String)
		if err != nil {
			return p, false, err
		}
		p.ConfirmedAt = &at
	}
	return p, true, nil
}

func (s *Store) LatestPlan(objectiveID string) (PlanRecord, bool, error) {
	if ok, err := s.hasTable("plans"); err != nil {
		return PlanRecord{}, false, err
	} else if !ok {
		return PlanRecord{}, false, nil
	}
	var version sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(version) FROM plans WHERE objective_id=?`, objectiveID).Scan(&version); err != nil {
		return PlanRecord{}, false, err
	}
	if !version.Valid || version.Int64 == 0 {
		return PlanRecord{}, false, nil
	}
	return s.GetPlan(objectiveID, int(version.Int64))
}

func (s *Store) ListPlans(objectiveID string) ([]PlanRecord, error) {
	rows, err := s.db.Query(`SELECT version FROM plans WHERE objective_id=? ORDER BY version`, objectiveID)
	if err != nil {
		return nil, err
	}
	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]PlanRecord, 0, len(versions))
	for _, v := range versions {
		p, _, err := s.GetPlan(objectiveID, v)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// SupersedeTasks retires unfinished tasks from an older plan version. Running
// tasks are never superseded silently; only pending/blocked ones are.
func (s *Store) SupersedeTasks(ids []domain.TaskID, reason string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, id := range ids {
		res, err := tx.Exec(`UPDATE tasks SET status='superseded', last_error=?
			WHERE id=? AND status IN ('pending','blocked')`, reason, string(id))
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		n += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// TasksForPlan lists tasks belonging to one plan version.
// TasksForPlan returns the tasks named by a plan version. Membership is decided
// by the plan itself, not by the version stamped on the task row: a continuation
// keeps accepted tasks from an earlier plan, and they are still part of this one.
func (s *Store) TasksForPlan(objectiveID string, version int) ([]domain.Task, error) {
	record, ok, err := s.GetPlan(objectiveID, version)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	members := map[domain.TaskID]bool{}
	for _, spec := range record.Spec.Tasks {
		members[domain.TaskID(spec.ID)] = true
	}
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []domain.Task
	for _, t := range all {
		if t.ObjectiveID == objectiveID && members[t.ID] {
			out = append(out, t)
		}
	}
	return out, nil
}

// ApplyReplan records the replacement plan, retires the tasks it drops, and
// activates the new version in one transaction. Doing it in steps leaves a
// window where retired tasks have no plan that owns them.
func (s *Store) ApplyReplan(objectiveID string, version int, spec goal.Plan, hash string,
	supersede []domain.TaskID, reason string) (int, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	planID := fmt.Sprintf("%s-plan-v%d", objectiveID, version)
	if _, err := tx.Exec(`INSERT INTO plans (id, objective_id, version, status, spec, hash, created_at)
		VALUES (?, ?, ?, 'draft', ?, ?, ?)`, planID, objectiveID, version, string(raw), hash, now()); err != nil {
		return 0, fmt.Errorf("create plan v%d: %w", version, err)
	}
	retired := 0
	for _, taskID := range supersede {
		res, err := tx.Exec(`UPDATE tasks SET status='superseded', last_error=?
			WHERE id=? AND status IN ('pending','blocked')`, reason, string(taskID))
		if err != nil {
			return 0, err
		}
		affected, _ := res.RowsAffected()
		retired += int(affected)
	}
	res, err := tx.Exec(`UPDATE plans SET status='confirmed', confirmed_at=?
		WHERE objective_id=? AND version=? AND status='draft'`, now(), objectiveID, version)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, fmt.Errorf("plan v%d is not a draft", version)
	}
	if _, err := tx.Exec(`UPDATE plans SET status='superseded'
		WHERE objective_id=? AND version<>? AND status='confirmed'`, objectiveID, version); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE objectives SET plan_version=?, status=?, updated_at=? WHERE id=?`,
		version, string(goal.StatusActive), now(), objectiveID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return retired, nil
}
