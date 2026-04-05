package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

type Event struct {
	ID        int64
	Source    string
	Type      string
	Payload   map[string]any
	CreatedAt time.Time
}

type Action struct {
	ID            int64
	EventID       int64
	SkillName     string
	LLMResponse   string
	ActionType    string
	ActionPayload map[string]any
	Status        string
	CreatedAt     time.Time
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	conn.SetMaxOpenConns(1)

	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}

	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source TEXT NOT NULL,
		type TEXT NOT NULL,
		payload TEXT DEFAULT '{}',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS actions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id INTEGER,
		skill_name TEXT NOT NULL,
		llm_response TEXT DEFAULT '',
		action_type TEXT NOT NULL,
		action_payload TEXT DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'pending',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (event_id) REFERENCES events(id)
	);

	CREATE TABLE IF NOT EXISTS memory (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		key TEXT UNIQUE NOT NULL,
		value TEXT DEFAULT '{}',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_events_source ON events(source);
	CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
	CREATE INDEX IF NOT EXISTS idx_actions_event ON actions(event_id);
	CREATE INDEX IF NOT EXISTS idx_actions_status ON actions(status);
	CREATE INDEX IF NOT EXISTS idx_memory_key ON memory(key);
	`
	_, err := db.conn.Exec(schema)
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}

func (db *DB) InsertEvent(source, typ string, payload map[string]any) (int64, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshaling payload: %w", err)
	}

	result, err := db.conn.Exec(
		"INSERT INTO events (source, type, payload) VALUES (?, ?, ?)",
		source, typ, string(data),
	)
	if err != nil {
		return 0, fmt.Errorf("inserting event: %w", err)
	}
	return result.LastInsertId()
}

func (db *DB) InsertAction(eventID int64, skillName, llmResponse, actionType string, payload map[string]any, status string) (int64, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshaling payload: %w", err)
	}

	result, err := db.conn.Exec(
		"INSERT INTO actions (event_id, skill_name, llm_response, action_type, action_payload, status) VALUES (?, ?, ?, ?, ?, ?)",
		eventID, skillName, llmResponse, actionType, string(data), status,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting action: %w", err)
	}
	return result.LastInsertId()
}

func (db *DB) UpdateActionStatus(id int64, status string) error {
	_, err := db.conn.Exec("UPDATE actions SET status = ? WHERE id = ?", status, id)
	return err
}

func (db *DB) RecentEvents(limit int) ([]Event, error) {
	rows, err := db.conn.Query(
		"SELECT id, source, type, payload, created_at FROM events ORDER BY created_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var payloadStr string
		if err := rows.Scan(&e.ID, &e.Source, &e.Type, &payloadStr, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning event: %w", err)
		}
		if err := json.Unmarshal([]byte(payloadStr), &e.Payload); err != nil {
			e.Payload = map[string]any{"raw": payloadStr}
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (db *DB) RecentActions(limit int) ([]Action, error) {
	rows, err := db.conn.Query(
		"SELECT id, event_id, skill_name, llm_response, action_type, action_payload, status, created_at FROM actions ORDER BY created_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying actions: %w", err)
	}
	defer rows.Close()

	var actions []Action
	for rows.Next() {
		var a Action
		var payloadStr string
		if err := rows.Scan(&a.ID, &a.EventID, &a.SkillName, &a.LLMResponse, &a.ActionType, &payloadStr, &a.Status, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning action: %w", err)
		}
		if err := json.Unmarshal([]byte(payloadStr), &a.ActionPayload); err != nil {
			a.ActionPayload = map[string]any{"raw": payloadStr}
		}
		actions = append(actions, a)
	}
	return actions, rows.Err()
}

func (db *DB) SetMemory(key, value string) error {
	_, err := db.conn.Exec(
		`INSERT INTO memory (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value,
	)
	return err
}

func (db *DB) GetMemory(key string) (string, error) {
	var value string
	err := db.conn.QueryRow("SELECT value FROM memory WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (db *DB) AllMemory() (map[string]string, error) {
	rows, err := db.conn.Query("SELECT key, value FROM memory ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	mem := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		mem[k] = v
	}
	return mem, rows.Err()
}
