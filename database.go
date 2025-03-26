package main

import (
	"database/sql"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Database struct {
	db *sql.DB
}

func NewDatabase(path string) (*Database, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	// Create tables if they don't exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS post_mappings (
			mastodon_id TEXT PRIMARY KEY,
			bluesky_ids TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS edits (
			edit_id TEXT PRIMARY KEY,
			original_id TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`)
	if err != nil {
		return nil, err
	}

	return &Database{db: db}, nil
}

func (d *Database) SavePostMapping(mastodonID string, bskyIDs []string) error {
	// Join all bluesky IDs with a comma
	idsStr := strings.Join(bskyIDs, ",")

	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO post_mappings (mastodon_id, bluesky_ids) VALUES (?, ?)",
		mastodonID, idsStr,
	)
	return err
}

func (d *Database) GetBlueskyIDsForMastodonPost(mastodonID string) ([]string, error) {
	var idsStr string
	err := d.db.QueryRow(
		"SELECT bluesky_ids FROM post_mappings WHERE mastodon_id = ?",
		mastodonID,
	).Scan(&idsStr)

	if err != nil {
		return nil, err
	}

	return strings.Split(idsStr, ","), nil
}

func (d *Database) CheckIfEdit(mastodonID string, originalID string) (string, bool) {
	// If we already know the original ID from Mastodon
	if originalID != "" && originalID != mastodonID {
		// Store this relationship for future reference
		d.MarkAsEdit(mastodonID, originalID)
		return originalID, true
	}

	// Check our database for known edits
	var origID string
	err := d.db.QueryRow(
		"SELECT original_id FROM edits WHERE edit_id = ?",
		mastodonID,
	).Scan(&origID)

	if err != nil {
		return "", false
	}

	return origID, true
}

func (d *Database) MarkAsEdit(editID, origID string) error {
	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO edits (edit_id, original_id) VALUES (?, ?)",
		editID, origID,
	)
	return err
}

func (d *Database) GetLastSeenID() (string, error) {
	var id string
	err := d.db.QueryRow(
		"SELECT value FROM state WHERE key = 'last_seen_id'",
	).Scan(&id)

	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}

	return id, nil
}

func (d *Database) SaveLastSeenID(id string) error {
	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO state (key, value) VALUES ('last_seen_id', ?)",
		id,
	)
	return err
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) GetBridgedPostIDs() ([]string, error) {
	rows, err := d.db.Query("SELECT DISTINCT mastodon_id FROM post_mappings")
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

	return ids, nil
}

func (d *Database) GetLastCheckTime() (time.Time, error) {
	var timeStr string
	err := d.db.QueryRow("SELECT value FROM state WHERE key = 'last_edit_check'").Scan(&timeStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}

	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return time.Time{}, err
	}

	return t, nil
}

func (d *Database) SaveLastCheckTime(t time.Time) error {
	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO state (key, value) VALUES ('last_edit_check', ?)",
		t.Format(time.RFC3339),
	)
	return err
}

func (d *Database) GetRecentPostsToCheckForEdits(maxCount int) ([]string, error) {
	rows, err := d.db.Query(
		"SELECT mastodon_id FROM post_mappings ORDER BY created_at DESC LIMIT ?",
		maxCount,
	)
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

	return ids, nil
}

// Add this to track the last edit time for a post
func (d *Database) SaveLastEditTime(postID string, editTime time.Time) error {
	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO state (key, value) VALUES (?, ?)",
		"edit_time_"+postID, editTime.Format(time.RFC3339),
	)
	return err
}

func (d *Database) GetLastEditTime(postID string) (time.Time, error) {
	var timeStr string
	err := d.db.QueryRow(
		"SELECT value FROM state WHERE key = ?",
		"edit_time_"+postID,
	).Scan(&timeStr)

	if err != nil {
		if err == sql.ErrNoRows {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}

	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return time.Time{}, err
	}

	return t, nil
}

func (d *Database) SaveContentHash(postID string, contentHash string) error {
	_, err := d.db.Exec(
		"INSERT OR REPLACE INTO state (key, value) VALUES (?, ?)",
		"content_hash_"+postID, contentHash,
	)
	return err
}

func (d *Database) GetContentHash(postID string) (string, error) {
	var hash string
	err := d.db.QueryRow(
		"SELECT value FROM state WHERE key = ?",
		"content_hash_"+postID,
	).Scan(&hash)

	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}

	return hash, nil
}
