// Package telegram contains the bot binary's command handlers, agent client,
// and auth allowlist.  This package owns its own sqlite store for the auth
// table; everything else is plain in-memory state.
package telegram

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// AuthUser is a single allowlisted user.
type AuthUser struct {
	TelegramID   int64
	CreatedAt    string
	LastActivity string
}

// AuthStore is an optional sqlite-backed allowlist.  When disabled all chats
// are accepted.  Mirrors integrations/telegram/auth.py.
type AuthStore struct {
	enabled bool
	adminID int64
	mu      sync.Mutex
	db      *sql.DB
}

// AuthFromEnv reads the same env vars as the Python version.
func AuthFromEnv() (*AuthStore, error) {
	enabled := boolEnv("BOT_AUTH_ENABLED", false)
	if !enabled {
		log.Printf("auth: disabled (BOT_AUTH_ENABLED is not true). Bot is open for everyone.")
		return &AuthStore{enabled: false}, nil
	}
	admin, _ := strconv.ParseInt(os.Getenv("BOT_ADMIN_ID"), 10, 64)
	if admin == 0 {
		return nil, errors.New("auth enabled but BOT_ADMIN_ID is 0")
	}
	dbPath := os.Getenv("BOT_AUTH_DB")
	if dbPath == "" {
		dbPath = "/app/data/telegram_auth.sqlite3"
	}
	if dir := filepath.Dir(dbPath); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			telegram_id   INTEGER PRIMARY KEY,
			created_at    TEXT NOT NULL,
			last_activity TEXT NOT NULL
		)`); err != nil {
		return nil, err
	}
	log.Printf("auth: ENABLED. Admin ID=%d, DB=%s", admin, dbPath)
	return &AuthStore{enabled: true, adminID: admin, db: db}, nil
}

func (a *AuthStore) IsEnabled() bool { return a.enabled }
func (a *AuthStore) AdminID() int64  { return a.adminID }

func (a *AuthStore) IsAdmin(id int64) bool { return a.enabled && id == a.adminID }

func (a *AuthStore) IsAuthorized(id int64) bool {
	if !a.enabled {
		return true
	}
	if id == a.adminID {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var x int
	err := a.db.QueryRow("SELECT 1 FROM users WHERE telegram_id = ?", id).Scan(&x)
	return err == nil
}

func (a *AuthStore) AddUser(id int64) error {
	if !a.enabled {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err := a.db.Exec(`
		INSERT INTO users (telegram_id, created_at, last_activity) VALUES (?, ?, ?)
		ON CONFLICT(telegram_id) DO UPDATE SET last_activity = excluded.last_activity`,
		id, now, now)
	return err
}

func (a *AuthStore) Touch(id int64) {
	if !a.enabled || id == a.adminID {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.db.Exec("UPDATE users SET last_activity = ? WHERE telegram_id = ?", now, id)
}

func (a *AuthStore) ListUsers(limit int) ([]AuthUser, error) {
	if !a.enabled {
		return nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	rows, err := a.db.Query(`
		SELECT telegram_id, created_at, last_activity
		FROM users ORDER BY created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthUser
	for rows.Next() {
		var u AuthUser
		if err := rows.Scan(&u.TelegramID, &u.CreatedAt, &u.LastActivity); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func boolEnv(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	return def
}
