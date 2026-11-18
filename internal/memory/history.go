package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type historyStore struct {
	db *sql.DB
}

func newHistoryStore(path string) (*historyStore, error) {
	// modernc.org/sqlite expects pragmas as _pragma=name(value); the
	// mattn-style _journal_mode/_busy_timeout keys are silently ignored.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
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
	// Added after the first release; the duplicate-column error is expected
	// on every later start.
	_, _ = db.Exec(`ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT ''`)
	if err := initTraceTables(db); err != nil {
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
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("session created but id unavailable: %w", err)
	}
	return int(id), nil
}

func (h *historyStore) listSessions(limit int) ([]SessionInfo, error) {
	return h.searchSessions(limit, "")
}

// searchSessions lists sessions newest first; a non-empty query keeps those
// whose title or any message contains it (case-insensitive). Each row
// carries the first user message as a preview and the last activity time.
func (h *historyStore) searchSessions(limit int, query string) ([]SessionInfo, error) {
	q := `
		SELECT s.id, s.created_at, s.title, COUNT(m.id), COALESCE(MAX(m.ts), s.created_at),
			COALESCE((SELECT content_json FROM messages f WHERE f.session_id = s.id AND f.role = 'user' ORDER BY f.ts LIMIT 1), '')
		FROM sessions s
		LEFT JOIN messages m ON m.session_id = s.id`
	args := []any{}
	if query = strings.TrimSpace(query); query != "" {
		like := "%" + escapeLike(strings.ToLower(query)) + "%"
		q += ` WHERE lower(s.title) LIKE ? ESCAPE '\' OR s.id IN (
			SELECT session_id FROM messages WHERE lower(content_json) LIKE ? ESCAPE '\')`
		args = append(args, like, like)
	}
	q += ` GROUP BY s.id ORDER BY s.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := h.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInfo
	for rows.Next() {
		var s SessionInfo
		var preview string
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.Title, &s.MessageCount, &s.LastAt, &preview); err != nil {
			return nil, err
		}
		s.Preview = previewText(unwrapContent(preview), 120)
		out = append(out, s)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func previewText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// MessageHit is one message that matched a history search.
type MessageHit struct {
	SessionID int     `json:"session_id"`
	Title     string  `json:"session_title,omitempty"`
	TS        float64 `json:"ts"`
	Role      string  `json:"role"`
	Snippet   string  `json:"snippet"`
}

// searchMessages finds user/assistant messages containing every word of
// query (case-insensitive), newest first, with a snippet around the first
// match. excludeSession leaves out the asking conversation itself.
func (h *historyStore) searchMessages(query string, limit, excludeSession int) ([]MessageHit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}
	if len(words) > 8 {
		words = words[:8]
	}
	q := `SELECT m.session_id, COALESCE(s.title, ''), m.ts, m.role, m.content_json
		FROM messages m LEFT JOIN sessions s ON s.id = m.session_id
		WHERE m.role IN ('user', 'assistant') AND m.session_id != ?`
	args := []any{excludeSession}
	for _, w := range words {
		q += ` AND lower(m.content_json) LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(w)+"%")
	}
	q += ` ORDER BY m.ts DESC LIMIT ?`
	args = append(args, limit)
	rows, err := h.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageHit
	for rows.Next() {
		var hit MessageHit
		var cj string
		if err := rows.Scan(&hit.SessionID, &hit.Title, &hit.TS, &hit.Role, &cj); err != nil {
			return nil, err
		}
		hit.Snippet = snippetAround(unwrapContent(cj), words[0], 240)
		out = append(out, hit)
	}
	return out, rows.Err()
}

// snippetAround returns about n characters of text centred on the first
// occurrence of word.
func snippetAround(text, word string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	i := strings.Index(strings.ToLower(text), word)
	at := 0
	if i > 0 {
		at = len([]rune(text[:i]))
	}
	start := max(at-n/3, 0)
	end := min(start+n, len(r))
	out := string(r[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
}

// renameSession sets a session's title; false when it does not exist.
func (h *historyStore) renameSession(id int, title string) (bool, error) {
	res, err := h.db.Exec(`UPDATE sessions SET title = ? WHERE id = ?`, strings.TrimSpace(title), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// deleteSession removes a session and its messages. Audit-log rows are kept
// on purpose: the audit trail of what tools ran must outlive the chat.
func (h *historyStore) deleteSession(id int) (bool, error) {
	tx, err := h.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_id = ?`, id); err != nil {
		return false, err
	}
	res, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, tx.Commit()
}

// UsageBucket is one time bucket of activity.
type UsageBucket struct {
	Start       float64 `json:"start"` // unix seconds
	Queries     int     `json:"queries"`
	ToolCalls   int     `json:"tool_calls"`
	ToolErrors  int     `json:"tool_errors"`
	AvgToolMS   float64 `json:"avg_tool_ms"`
	TotalToolMS int64   `json:"-"`
}

// SkillUsage summarises one skill over the window.
type SkillUsage struct {
	Skill  string  `json:"skill"`
	Calls  int     `json:"calls"`
	Errors int     `json:"errors"`
	AvgMS  float64 `json:"avg_ms"`
}

// Usage is the activity report behind the Health page's charts.
type Usage struct {
	Since   float64       `json:"since"`
	Bucket  string        `json:"bucket"` // hour | day
	Buckets []UsageBucket `json:"buckets"`
	Skills  []SkillUsage  `json:"skills"`
}

// usage aggregates user messages (queries) and audited tool calls since
// `since`, in buckets of bucketSec seconds (empty buckets included).
func (h *historyStore) usage(since time.Time, bucketSec int64, bucketName string) (*Usage, error) {
	start := since.Unix() / bucketSec * bucketSec
	now := time.Now().Unix()
	n := int((now-start)/bucketSec) + 1
	buckets := make([]UsageBucket, n)
	for i := range buckets {
		buckets[i].Start = float64(start + int64(i)*bucketSec)
	}
	idx := func(ts float64) int {
		i := int((int64(ts) - start) / bucketSec)
		if i < 0 || i >= n {
			return -1
		}
		return i
	}
	rows, err := h.db.Query(`SELECT ts FROM messages WHERE role = 'user' AND ts >= ?`, float64(start))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ts float64
		if err := rows.Scan(&ts); err != nil {
			rows.Close()
			return nil, err
		}
		if i := idx(ts); i >= 0 {
			buckets[i].Queries++
		}
	}
	rows.Close()

	rows, err = h.db.Query(`SELECT ts, skill, COALESCE(error_msg, '') != '', duration_ms FROM skill_invocations WHERE ts >= ?`, float64(start))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	per := map[string]*SkillUsage{}
	perMS := map[string]int64{}
	for rows.Next() {
		var ts float64
		var skill string
		var failed bool
		var ms int64
		if err := rows.Scan(&ts, &skill, &failed, &ms); err != nil {
			return nil, err
		}
		if i := idx(ts); i >= 0 {
			buckets[i].ToolCalls++
			buckets[i].TotalToolMS += ms
			if failed {
				buckets[i].ToolErrors++
			}
		}
		u := per[skill]
		if u == nil {
			u = &SkillUsage{Skill: skill}
			per[skill] = u
		}
		u.Calls++
		perMS[skill] += ms
		if failed {
			u.Errors++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range buckets {
		if buckets[i].ToolCalls > 0 {
			buckets[i].AvgToolMS = float64(buckets[i].TotalToolMS) / float64(buckets[i].ToolCalls)
		}
	}
	skills := make([]SkillUsage, 0, len(per))
	for name, u := range per {
		u.AvgMS = float64(perMS[name]) / float64(u.Calls)
		skills = append(skills, *u)
	}
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Calls != skills[j].Calls {
			return skills[i].Calls > skills[j].Calls
		}
		return skills[i].Skill < skills[j].Skill
	})
	return &Usage{Since: float64(start), Bucket: bucketName, Buckets: buckets, Skills: skills}, nil
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
	// Select the LAST n messages (the conversation tail), then re-order them
	// chronologically. A plain ORDER BY ts ASC LIMIT n would return the first
	// n messages and silently drop everything recent in long sessions.
	rows, err := h.db.Query(`
		SELECT role, content_json FROM (
			SELECT id, role, content_json, ts FROM messages
			WHERE session_id=? ORDER BY ts DESC, id DESC LIMIT ?
		) ORDER BY ts ASC, id ASC`, sessionID, limit)
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
