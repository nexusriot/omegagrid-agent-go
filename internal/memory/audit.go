package memory

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// AuditRecord is one persisted skill/tool invocation.
type AuditRecord struct {
	ID           int64   `json:"id"`
	SessionID    int     `json:"session_id"`
	Step         int     `json:"step"`
	TS           float64 `json:"ts"`
	Skill        string  `json:"skill"`
	Kind         string  `json:"kind"` // skill | tool | unknown | replay
	Args         any     `json:"args"`
	Result       any     `json:"result,omitempty"`
	ErrorMsg     string  `json:"error,omitempty"`
	DurationMS   int64   `json:"duration_ms"`
	Why          string  `json:"why,omitempty"`
	ReplayedFrom *int64  `json:"replayed_from,omitempty"`
}

// AuditFilter controls which rows ListInvocations returns.
type AuditFilter struct {
	SessionID  int
	Skill      string
	Since      float64
	Until      float64
	OnlyErrors bool
	Limit      int
	Offset     int
}

func (h *historyStore) recordInvocation(r AuditRecord, maxBlob int) error {
	argsJSON := marshalAuditBlob(r.Args, maxBlob)
	var resultJSON *string
	if r.Result != nil {
		s := marshalAuditBlob(r.Result, maxBlob)
		resultJSON = &s
	}
	var errMsg, why *string
	if r.ErrorMsg != "" {
		errMsg = &r.ErrorMsg
	}
	if r.Why != "" {
		why = &r.Why
	}
	ts := float64(time.Now().UnixMilli()) / 1000.0
	_, err := h.db.Exec(
		`INSERT INTO skill_invocations
		 (session_id,step,ts,skill,kind,args_json,result_json,error_msg,duration_ms,why,replayed_from)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		r.SessionID, r.Step, ts, r.Skill, r.Kind,
		argsJSON, resultJSON, errMsg, r.DurationMS, why, r.ReplayedFrom,
	)
	return err
}

func (h *historyStore) getInvocation(id int64) (*AuditRecord, error) {
	row := h.db.QueryRow(
		`SELECT id,session_id,step,ts,skill,kind,args_json,result_json,error_msg,duration_ms,why,replayed_from
		 FROM skill_invocations WHERE id=?`, id)
	rec, err := scanInvocation(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (h *historyStore) listInvocations(f AuditFilter) ([]AuditRecord, int, error) {
	where, args := buildAuditWhere(f)

	var total int
	if err := h.db.QueryRow("SELECT COUNT(*) FROM skill_invocations"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := h.db.Query(
		`SELECT id,session_id,step,ts,skill,kind,args_json,result_json,error_msg,duration_ms,why,replayed_from
		 FROM skill_invocations`+where+` ORDER BY ts DESC LIMIT ? OFFSET ?`,
		append(args, limit, f.Offset)...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	recs, err := scanInvocationRows(rows)
	return recs, total, err
}

func buildAuditWhere(f AuditFilter) (string, []any) {
	var clauses []string
	var args []any
	if f.SessionID > 0 {
		clauses = append(clauses, "session_id=?")
		args = append(args, f.SessionID)
	}
	if f.Skill != "" {
		clauses = append(clauses, "skill=?")
		args = append(args, f.Skill)
	}
	if f.Since > 0 {
		clauses = append(clauses, "ts>=?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		clauses = append(clauses, "ts<=?")
		args = append(args, f.Until)
	}
	if f.OnlyErrors {
		clauses = append(clauses, "error_msg IS NOT NULL")
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func scanInvocation(scan func(...any) error) (AuditRecord, error) {
	var r AuditRecord
	var argsJSON string
	var resultJSON, errMsg, why sql.NullString
	var replayedFrom sql.NullInt64
	if err := scan(
		&r.ID, &r.SessionID, &r.Step, &r.TS, &r.Skill, &r.Kind,
		&argsJSON, &resultJSON, &errMsg, &r.DurationMS, &why, &replayedFrom,
	); err != nil {
		return r, err
	}
	r.Args = unmarshalAuditBlob(argsJSON)
	if resultJSON.Valid {
		r.Result = unmarshalAuditBlob(resultJSON.String)
	}
	if errMsg.Valid {
		r.ErrorMsg = errMsg.String
	}
	if why.Valid {
		r.Why = why.String
	}
	if replayedFrom.Valid {
		v := replayedFrom.Int64
		r.ReplayedFrom = &v
	}
	return r, nil
}

func scanInvocationRows(rows *sql.Rows) ([]AuditRecord, error) {
	var out []AuditRecord
	for rows.Next() {
		rec, err := scanInvocation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func marshalAuditBlob(v any, maxBytes int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"marshal failed"}`
	}
	if maxBytes > 0 && len(b) > maxBytes {
		return string(b[:maxBytes])
	}
	return string(b)
}

func unmarshalAuditBlob(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}
