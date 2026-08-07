package db

// Tile is a clickable service link shown on the dashboard grid.
type Tile struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
	Position    int    `json:"position"`
	CreatedAt   string `json:"created_at"`
}

// Note is a free-form text card.
type Note struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Todo is a single checklist item.
type Todo struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	Done      bool   `json:"done"`
	Position  int    `json:"position"`
	CreatedAt string `json:"created_at"`
}

// --- Tiles ---

func (s *Store) ListTiles() ([]Tile, error) {
	rows, err := s.db.Query(`SELECT id, title, url, icon, description, position, created_at FROM tiles ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tiles := []Tile{}
	for rows.Next() {
		var t Tile
		if err := rows.Scan(&t.ID, &t.Title, &t.URL, &t.Icon, &t.Description, &t.Position, &t.CreatedAt); err != nil {
			return nil, err
		}
		tiles = append(tiles, t)
	}
	return tiles, rows.Err()
}

func (s *Store) getTile(id int64) (Tile, error) {
	var t Tile
	err := s.db.QueryRow(`SELECT id, title, url, icon, description, position, created_at FROM tiles WHERE id = ?`, id).
		Scan(&t.ID, &t.Title, &t.URL, &t.Icon, &t.Description, &t.Position, &t.CreatedAt)
	return t, err
}

func (s *Store) CreateTile(t Tile) (Tile, error) {
	res, err := s.db.Exec(`INSERT INTO tiles (title, url, icon, description, position) VALUES (?, ?, ?, ?, ?)`,
		t.Title, t.URL, t.Icon, t.Description, t.Position)
	if err != nil {
		return Tile{}, err
	}
	id, _ := res.LastInsertId()
	return s.getTile(id)
}

func (s *Store) UpdateTile(t Tile) (Tile, error) {
	if _, err := s.db.Exec(`UPDATE tiles SET title=?, url=?, icon=?, description=?, position=? WHERE id=?`,
		t.Title, t.URL, t.Icon, t.Description, t.Position, t.ID); err != nil {
		return Tile{}, err
	}
	return s.getTile(t.ID)
}

func (s *Store) DeleteTile(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tiles WHERE id=?`, id)
	return err
}

// --- Notes ---

func (s *Store) ListNotes() ([]Note, error) {
	rows, err := s.db.Query(`SELECT id, body, created_at, updated_at FROM notes ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notes := []Note{}
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Body, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

func (s *Store) getNote(id int64) (Note, error) {
	var n Note
	err := s.db.QueryRow(`SELECT id, body, created_at, updated_at FROM notes WHERE id = ?`, id).
		Scan(&n.ID, &n.Body, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}

func (s *Store) CreateNote(n Note) (Note, error) {
	res, err := s.db.Exec(`INSERT INTO notes (body) VALUES (?)`, n.Body)
	if err != nil {
		return Note{}, err
	}
	id, _ := res.LastInsertId()
	return s.getNote(id)
}

func (s *Store) UpdateNote(n Note) (Note, error) {
	if _, err := s.db.Exec(`UPDATE notes SET body=?, updated_at=datetime('now') WHERE id=?`, n.Body, n.ID); err != nil {
		return Note{}, err
	}
	return s.getNote(n.ID)
}

func (s *Store) DeleteNote(id int64) error {
	_, err := s.db.Exec(`DELETE FROM notes WHERE id=?`, id)
	return err
}

// --- Todos ---

func (s *Store) ListTodos() ([]Todo, error) {
	rows, err := s.db.Query(`SELECT id, text, done, position, created_at FROM todos ORDER BY done, position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	todos := []Todo{}
	for rows.Next() {
		var (
			t    Todo
			done int
		)
		if err := rows.Scan(&t.ID, &t.Text, &done, &t.Position, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Done = done != 0
		todos = append(todos, t)
	}
	return todos, rows.Err()
}

func (s *Store) getTodo(id int64) (Todo, error) {
	var (
		t    Todo
		done int
	)
	err := s.db.QueryRow(`SELECT id, text, done, position, created_at FROM todos WHERE id = ?`, id).
		Scan(&t.ID, &t.Text, &done, &t.Position, &t.CreatedAt)
	t.Done = done != 0
	return t, err
}

func (s *Store) CreateTodo(t Todo) (Todo, error) {
	res, err := s.db.Exec(`INSERT INTO todos (text, done, position) VALUES (?, ?, ?)`,
		t.Text, b2i(t.Done), t.Position)
	if err != nil {
		return Todo{}, err
	}
	id, _ := res.LastInsertId()
	return s.getTodo(id)
}

func (s *Store) UpdateTodo(t Todo) (Todo, error) {
	if _, err := s.db.Exec(`UPDATE todos SET text=?, done=?, position=? WHERE id=?`,
		t.Text, b2i(t.Done), t.Position, t.ID); err != nil {
		return Todo{}, err
	}
	return s.getTodo(t.ID)
}

func (s *Store) DeleteTodo(id int64) error {
	_, err := s.db.Exec(`DELETE FROM todos WHERE id=?`, id)
	return err
}
