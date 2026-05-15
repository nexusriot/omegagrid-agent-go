package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type historyStore struct {
	db *sql.DB
}

func newHistoryStore(path string) (*historyStore, error) {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sessions (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at REAL    NOT NULL
		);
		CREATE TABLE IF NOT EXISTS messages (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id INTEGER NOT NULL REFERENCES sessions(id),
			ts         REAL    NOT NULL,
			role       TEXT    NOT NULL,
			content_json TEXT  NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_msg_sess_ts ON messages(session_id, ts);
		CREATE TABLE IF NOT EXISTS skill_invocations (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id    INTEGER REFERENCES sessions(id),
			step          INTEGER NOT NULL,
			ts            REAL    NOT NULL,
			skill         TEXT    NOT NULL,
			kind          TEXT    NOT NULL,
			args_json     TEXT    NOT NULL,
			result_json   TEXT,
			error_msg     TEXT,
			duration_ms   INTEGER NOT NULL,
			why           TEXT,
			replayed_from INTEGER
		);
		CREATE INDEX IF NOT EXISTS idx_inv_session_ts ON skill_invocations(session_id, ts);
		CREATE INDEX IF NOT EXISTS idx_inv_skill_ts   ON skill_invocations(skill, ts);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &historyStore{db: db}, nil
}

func (h *historyStore) createSession() (int, error) {
	res, err := h.db.Exec("INSERT INTO sessions(created_at) VALUES(?)", float64(time.Now().UnixMilli())/1000.0)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return int(id), nil
}

func (h *historyStore) listSessions(limit int) ([]SessionInfo, error) {
	rows, err := h.db.Query(`
		SELECT s.id, s.created_at, COUNT(m.id)
		FROM sessions s
		LEFT JOIN messages m ON m.session_id = s.id
		GROUP BY s.id ORDER BY s.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInfo
	for rows.Next() {
		var s SessionInfo
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.MessageCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *historyStore) listMessages(sessionID, limit, offset int) ([]StoredMessage, error) {
	rows, err := h.db.Query(`
		SELECT id, session_id, ts, role, content_json
		FROM messages WHERE session_id=?
		ORDER BY ts ASC LIMIT ? OFFSET ?`, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredMessage
	for rows.Next() {
		var m StoredMessage
		var cj string
		if err := rows.Scan(&m.ID, &m.SessionID, &m.TS, &m.Role, &cj); err != nil {
			return nil, err
		}
		m.Content = unwrapContent(cj)
		if m.Content == "" {
			continue // skip raw_model_json and empty entries
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (h *historyStore) loadTail(sessionID, limit int) ([]Message, error) {
	rows, err := h.db.Query(`
		SELECT role, content_json FROM messages
		WHERE session_id=? ORDER BY ts ASC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var role, cj string
		if err := rows.Scan(&role, &cj); err != nil {
			return nil, err
		}
		content := unwrapContent(cj)
		if content == "" {
			continue
		}
		out = append(out, Message{Role: role, Content: content})
	}
	return out, rows.Err()
}

func (h *historyStore) addMessage(sessionID int, role string, content any) error {
	cj, err := marshalContent(content)
	if err != nil {
		return fmt.Errorf("marshal message content: %w", err)
	}
	_, err = h.db.Exec(
		"INSERT INTO messages(session_id, ts, role, content_json) VALUES(?,?,?,?)",
		sessionID, float64(time.Now().UnixMilli())/1000.0, role, cj,
	)
	return err
}

func (h *historyStore) close() error { return h.db.Close() }

// marshalContent mirrors the Python sidecar: strings are wrapped in {"content":"..."},
// everything else is JSON-serialised as-is.
func marshalContent(v any) (string, error) {
	switch x := v.(type) {
	case string:
		b, err := json.Marshal(map[string]any{"content": x})
		return string(b), err
	default:
		b, err := json.Marshal(v)
		return string(b), err
	}
}

// unwrapContent mirrors Python's load_tail logic:
//   - skip {"raw_model_json": ...} debug entries
//   - unwrap {"content": "..."} single-key payloads to plain strings
//   - leave multi-key payloads as JSON
func unwrapContent(cj string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(cj), &m); err != nil {
		return cj
	}
	if _, ok := m["raw_model_json"]; ok {
		return ""
	}
	if len(m) == 1 {
		if c, ok := m["content"]; ok {
			switch x := c.(type) {
			case string:
				return x
			default:
				b, _ := json.Marshal(x)
				return string(b)
			}
		}
	}
	return cj
}
