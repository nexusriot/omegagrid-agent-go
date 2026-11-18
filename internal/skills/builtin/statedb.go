package builtin

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // pure-Go SQLite driver ("sqlite")
)

// StateDB is the SQLite file the stateful skills (uptime_check history,
// todo) keep their rows in. One file, opened lazily on first use and shared
// by every skill that names the same path.
type StateDB struct {
	Path string
}

var (
	stateMu  sync.Mutex
	stateDBs = map[string]*sql.DB{}
)

// open returns the shared handle for s.Path, running schema (idempotent DDL)
// once per process for each distinct schema string.
func (s StateDB) open(schema string) (*sql.DB, error) {
	if s.Path == "" {
		return nil, errors.New("no state database is configured (SKILL_STATE_DB)")
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	db, ok := stateDBs[s.Path]
	if !ok {
		if dir := filepath.Dir(s.Path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create state dir: %w", err)
			}
		}
		var err error
		db, err = sql.Open("sqlite", s.Path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		stateDBs[s.Path] = db
	}
	key := s.Path + "\x00" + schema
	if !schemaDone[key] {
		if _, err := db.Exec(schema); err != nil {
			return nil, fmt.Errorf("state schema: %w", err)
		}
		schemaDone[key] = true
	}
	return db, nil
}

var schemaDone = map[string]bool{}
