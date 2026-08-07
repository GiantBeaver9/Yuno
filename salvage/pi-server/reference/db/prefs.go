package db

import "database/sql"

// Location is a saved weather location for a Telegram user.
type Location struct {
	ID    int64   `json:"id"`
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
}

// --- digest subscription (per Telegram chat) ---

// SetDigest turns the daily digest on/off for a chat (upsert).
func (s *Store) SetDigest(chatID int64, on bool) error {
	_, err := s.db.Exec(`INSERT INTO bot_prefs (chat_id, digest) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET digest=excluded.digest`, chatID, b2i(on))
	return err
}

// DigestEnabled reports whether a chat is subscribed to the digest (default off).
func (s *Store) DigestEnabled(chatID int64) (bool, error) {
	var d int
	err := s.db.QueryRow(`SELECT digest FROM bot_prefs WHERE chat_id=?`, chatID).Scan(&d)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return d != 0, err
}

// DigestSubscribers returns the chat ids opted in to the daily digest.
func (s *Store) DigestSubscribers() ([]int64, error) {
	rows, err := s.db.Query(`SELECT chat_id FROM bot_prefs WHERE digest=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- per-user weather locations ---

// AddUserLocation saves a weather location for a chat and returns it.
func (s *Store) AddUserLocation(chatID int64, label string, lat, lon float64) (Location, error) {
	res, err := s.db.Exec(`INSERT INTO user_locations (chat_id, label, lat, lon) VALUES (?, ?, ?, ?)`,
		chatID, label, lat, lon)
	if err != nil {
		return Location{}, err
	}
	id, _ := res.LastInsertId()
	return Location{ID: id, Label: label, Lat: lat, Lon: lon}, nil
}

// RemoveUserLocation deletes a chat's location by label (case-insensitive).
// ok is false when no such label existed.
func (s *Store) RemoveUserLocation(chatID int64, label string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM user_locations WHERE chat_id=? AND label=? COLLATE NOCASE`, chatID, label)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UserLocations returns a chat's saved weather locations, oldest first.
func (s *Store) UserLocations(chatID int64) ([]Location, error) {
	rows, err := s.db.Query(`SELECT id, label, lat, lon FROM user_locations WHERE chat_id=? ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locs := []Location{}
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.ID, &l.Label, &l.Lat, &l.Lon); err != nil {
			return nil, err
		}
		locs = append(locs, l)
	}
	return locs, rows.Err()
}
