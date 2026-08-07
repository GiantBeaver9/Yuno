package db

import "database/sql"

// Types for the per-user work tracker: projects (with point-in-time updates),
// self check-ins, and goals. owner_id scopes every row to a user (the Telegram
// chat id for now). OwnerID is never exposed in JSON.

type Project struct {
	ID              int64           `json:"id"`
	OwnerID         int64           `json:"-"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	PercentComplete float64         `json:"percentComplete"`
	EstEngHours     float64         `json:"estEngHours"`
	EstAgentHours   float64         `json:"estAgentHours"`
	Bugs            int             `json:"bugs"`
	FirstPass       bool            `json:"firstPass"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
	Updates         []ProjectUpdate `json:"updates,omitempty"`
}

type ProjectUpdate struct {
	ID              int64   `json:"id"`
	ProjectID       int64   `json:"projectId"`
	PercentComplete float64 `json:"percentComplete"`
	EstEngHours     float64 `json:"estEngHours"`
	EstAgentHours   float64 `json:"estAgentHours"`
	ETA             string  `json:"eta"`
	Notes           string  `json:"notes"`
	BugsFound       int     `json:"bugsFound"`
	BugsFixed       int     `json:"bugsFixed"`
	CreatedAt       string  `json:"createdAt"`
}

type Checkin struct {
	ID        int64  `json:"id"`
	OwnerID   int64  `json:"-"`
	Mood      int    `json:"mood"`
	Energy    int    `json:"energy"`
	Focus     int    `json:"focus"`
	Note      string `json:"note"`
	CreatedAt string `json:"createdAt"`
}

type Goal struct {
	ID         int64  `json:"id"`
	OwnerID    int64  `json:"-"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	TargetDate string `json:"targetDate"`
	Done       bool   `json:"done"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

// --- projects ---

const projectCols = `id, owner_id, name, description, percent_complete, est_eng_hours, est_agent_hours, bugs, first_pass, created_at, updated_at`

func scanProject(sc interface{ Scan(...any) error }) (Project, error) {
	var (
		p         Project
		firstPass int
	)
	err := sc.Scan(&p.ID, &p.OwnerID, &p.Name, &p.Description, &p.PercentComplete,
		&p.EstEngHours, &p.EstAgentHours, &p.Bugs, &firstPass, &p.CreatedAt, &p.UpdatedAt)
	p.FirstPass = firstPass != 0
	return p, err
}

func (s *Store) CreateProject(p Project) (Project, error) {
	res, err := s.db.Exec(`INSERT INTO projects (owner_id, name, description, percent_complete, est_eng_hours, est_agent_hours, bugs, first_pass) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.OwnerID, p.Name, p.Description, p.PercentComplete, p.EstEngHours, p.EstAgentHours, p.Bugs, b2i(p.FirstPass))
	if err != nil {
		return Project{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetProject(p.OwnerID, id)
}

// ListProjects returns a user's projects (newest first) without nested updates.
func (s *Store) ListProjects(ownerID int64) ([]Project, error) {
	rows, err := s.db.Query(`SELECT `+projectCols+` FROM projects WHERE owner_id=? ORDER BY created_at DESC, id DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject returns one project (with its updates, newest first), scoped to the
// owner. sql.ErrNoRows when it doesn't exist or belongs to someone else.
func (s *Store) GetProject(ownerID, id int64) (Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id=? AND owner_id=?`, id, ownerID))
	if err != nil {
		return Project{}, err
	}
	rows, err := s.db.Query(`SELECT id, project_id, percent_complete, est_eng_hours, est_agent_hours, eta, notes, bugs_found, bugs_fixed, created_at FROM project_updates WHERE project_id=? ORDER BY created_at DESC, id DESC`, id)
	if err != nil {
		return Project{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var u ProjectUpdate
		if err := rows.Scan(&u.ID, &u.ProjectID, &u.PercentComplete, &u.EstEngHours, &u.EstAgentHours, &u.ETA, &u.Notes, &u.BugsFound, &u.BugsFixed, &u.CreatedAt); err != nil {
			return Project{}, err
		}
		p.Updates = append(p.Updates, u)
	}
	return p, rows.Err()
}

// AddProjectUpdate inserts an update and rolls the given figures onto the parent
// project (bugs = previous open bugs + found - fixed, floored at 0). apply* say
// which numeric fields the caller actually supplied, so a partial update doesn't
// zero the project's headline figures. Scoped to the owner.
func (s *Store) AddProjectUpdate(ownerID, projectID int64, u ProjectUpdate, applyPct, applyEng, applyAgent bool) (Project, error) {
	p, err := s.GetProject(ownerID, projectID)
	if err != nil {
		return Project{}, err
	}
	if _, err := s.db.Exec(`INSERT INTO project_updates (project_id, percent_complete, est_eng_hours, est_agent_hours, eta, notes, bugs_found, bugs_fixed) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, u.PercentComplete, u.EstEngHours, u.EstAgentHours, u.ETA, u.Notes, u.BugsFound, u.BugsFixed); err != nil {
		return Project{}, err
	}
	if applyPct {
		p.PercentComplete = clampPct(u.PercentComplete)
	}
	if applyEng {
		p.EstEngHours = u.EstEngHours
	}
	if applyAgent {
		p.EstAgentHours = u.EstAgentHours
	}
	p.Bugs += u.BugsFound - u.BugsFixed
	if p.Bugs < 0 {
		p.Bugs = 0
	}
	if _, err := s.db.Exec(`UPDATE projects SET percent_complete=?, est_eng_hours=?, est_agent_hours=?, bugs=?, updated_at=datetime('now') WHERE id=? AND owner_id=?`,
		p.PercentComplete, p.EstEngHours, p.EstAgentHours, p.Bugs, projectID, ownerID); err != nil {
		return Project{}, err
	}
	return s.GetProject(ownerID, projectID)
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// --- check-ins ---

func (s *Store) LogCheckin(c Checkin) (Checkin, error) {
	res, err := s.db.Exec(`INSERT INTO checkins (owner_id, mood, energy, focus, note) VALUES (?, ?, ?, ?, ?)`,
		c.OwnerID, c.Mood, c.Energy, c.Focus, c.Note)
	if err != nil {
		return Checkin{}, err
	}
	id, _ := res.LastInsertId()
	err = s.db.QueryRow(`SELECT id, owner_id, mood, energy, focus, note, created_at FROM checkins WHERE id=?`, id).
		Scan(&c.ID, &c.OwnerID, &c.Mood, &c.Energy, &c.Focus, &c.Note, &c.CreatedAt)
	return c, err
}

// ListCheckins returns a user's most recent check-ins (newest first).
func (s *Store) ListCheckins(ownerID int64, limit int) ([]Checkin, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.Query(`SELECT id, owner_id, mood, energy, focus, note, created_at FROM checkins WHERE owner_id=? ORDER BY created_at DESC, id DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Checkin{}
	for rows.Next() {
		var c Checkin
		if err := rows.Scan(&c.ID, &c.OwnerID, &c.Mood, &c.Energy, &c.Focus, &c.Note, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- goals ---

func (s *Store) CreateGoal(g Goal) (Goal, error) {
	res, err := s.db.Exec(`INSERT INTO goals (owner_id, title, detail, target_date) VALUES (?, ?, ?, ?)`,
		g.OwnerID, g.Title, g.Detail, g.TargetDate)
	if err != nil {
		return Goal{}, err
	}
	id, _ := res.LastInsertId()
	return s.getGoal(g.OwnerID, id)
}

func (s *Store) getGoal(ownerID, id int64) (Goal, error) {
	var (
		g    Goal
		done int
	)
	err := s.db.QueryRow(`SELECT id, owner_id, title, detail, target_date, done, created_at, updated_at FROM goals WHERE id=? AND owner_id=?`, id, ownerID).
		Scan(&g.ID, &g.OwnerID, &g.Title, &g.Detail, &g.TargetDate, &done, &g.CreatedAt, &g.UpdatedAt)
	g.Done = done != 0
	return g, err
}

// ListGoals returns a user's goals, open first then by target date.
func (s *Store) ListGoals(ownerID int64) ([]Goal, error) {
	rows, err := s.db.Query(`SELECT id, owner_id, title, detail, target_date, done, created_at, updated_at FROM goals WHERE owner_id=? ORDER BY done, (target_date=''), target_date, id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Goal{}
	for rows.Next() {
		var (
			g    Goal
			done int
		)
		if err := rows.Scan(&g.ID, &g.OwnerID, &g.Title, &g.Detail, &g.TargetDate, &done, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		g.Done = done != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetGoalDone marks a goal done/undone, scoped to the owner. ok is false when no
// such goal exists for the user.
func (s *Store) SetGoalDone(ownerID, id int64, done bool) (Goal, bool, error) {
	res, err := s.db.Exec(`UPDATE goals SET done=?, updated_at=datetime('now') WHERE id=? AND owner_id=?`, b2i(done), id, ownerID)
	if err != nil {
		return Goal{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Goal{}, false, nil
	}
	g, err := s.getGoal(ownerID, id)
	if err == sql.ErrNoRows {
		return Goal{}, false, nil
	}
	return g, true, err
}
