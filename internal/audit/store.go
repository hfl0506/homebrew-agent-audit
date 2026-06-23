package audit

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Session struct {
	ID        int64      `json:"id"`
	Agent     string     `json:"agent"`
	Argv      []string   `json:"argv"`
	Cwd       string     `json:"cwd"`
	RepoRoot  string     `json:"repo_root,omitempty"`
	GitBranch string     `json:"git_branch,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
}

type TranscriptChunk struct {
	ID        int64     `json:"id"`
	SessionID int64     `json:"session_id"`
	TS        time.Time `json:"ts"`
	Stream    string    `json:"stream"`
	Content   string    `json:"content"`
}

type Event struct {
	ID        int64           `json:"id"`
	SessionID int64           `json:"session_id"`
	TS        time.Time       `json:"ts"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;`); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS sessions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent TEXT NOT NULL,
	argv_json TEXT NOT NULL,
	cwd TEXT NOT NULL,
	repo_root TEXT NOT NULL DEFAULT '',
	git_branch TEXT NOT NULL DEFAULT '',
	started_at TEXT NOT NULL,
	ended_at TEXT,
	exit_code INTEGER
);

CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	ts TEXT NOT NULL,
	type TEXT NOT NULL,
	payload_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS prompts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	ts TEXT NOT NULL,
	role TEXT NOT NULL,
	content TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS commands (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	ts_start TEXT NOT NULL,
	ts_end TEXT,
	cwd TEXT NOT NULL,
	argv_json TEXT NOT NULL,
	exit_code INTEGER
);

CREATE TABLE IF NOT EXISTS transcript_chunks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	ts TEXT NOT NULL,
	stream TEXT NOT NULL,
	content TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_session_ts ON events(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_transcript_session_ts ON transcript_chunks(session_id, ts);
`)
	return err
}

func (s *Store) CreateSession(session Session) (int64, error) {
	argvJSON, err := json.Marshal(session.Argv)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`
INSERT INTO sessions(agent, argv_json, cwd, repo_root, git_branch, started_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		session.Agent,
		string(argvJSON),
		session.Cwd,
		session.RepoRoot,
		session.GitBranch,
		formatTime(session.StartedAt),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishSession(sessionID int64, ended time.Time, exitCode int) error {
	_, err := s.db.Exec(`UPDATE sessions SET ended_at = ?, exit_code = ? WHERE id = ?`, formatTime(ended), exitCode, sessionID)
	return err
}

func (s *Store) InsertEvent(sessionID int64, typ string, payload any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO events(session_id, ts, type, payload_json)
VALUES (?, ?, ?, ?)`, sessionID, formatTime(time.Now()), typ, string(payloadJSON))
	return err
}

func (s *Store) InsertTranscript(sessionID int64, stream string, content []byte) error {
	if len(content) == 0 {
		return nil
	}
	_, err := s.db.Exec(`
INSERT INTO transcript_chunks(session_id, ts, stream, content)
VALUES (?, ?, ?, ?)`, sessionID, formatTime(time.Now()), stream, string(content))
	return err
}

func (s *Store) ListSessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT id, agent, argv_json, cwd, repo_root, git_branch, started_at, ended_at, exit_code
FROM sessions
ORDER BY id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) GetSessionWithTranscript(sessionID int64) (Session, []TranscriptChunk, error) {
	row := s.db.QueryRow(`
SELECT id, agent, argv_json, cwd, repo_root, git_branch, started_at, ended_at, exit_code
FROM sessions
WHERE id = ?`, sessionID)
	session, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, nil, fmt.Errorf("session %d not found", sessionID)
		}
		return Session{}, nil, err
	}

	rows, err := s.db.Query(`
SELECT id, session_id, ts, stream, content
FROM transcript_chunks
WHERE session_id = ?
ORDER BY id ASC`, sessionID)
	if err != nil {
		return Session{}, nil, err
	}
	defer rows.Close()

	var chunks []TranscriptChunk
	for rows.Next() {
		var chunk TranscriptChunk
		var ts string
		if err := rows.Scan(&chunk.ID, &chunk.SessionID, &ts, &chunk.Stream, &chunk.Content); err != nil {
			return Session{}, nil, err
		}
		chunk.TS, err = parseTime(ts)
		if err != nil {
			return Session{}, nil, err
		}
		chunks = append(chunks, chunk)
	}
	return session, chunks, rows.Err()
}

func (s *Store) ExportSessionJSON(w io.Writer, sessionID int64) error {
	session, chunks, err := s.GetSessionWithTranscript(sessionID)
	if err != nil {
		return err
	}
	events, err := s.listEvents(sessionID)
	if err != nil {
		return err
	}
	payload := struct {
		Session    Session           `json:"session"`
		Events     []Event           `json:"events"`
		Transcript []TranscriptChunk `json:"transcript"`
	}{
		Session:    session,
		Events:     events,
		Transcript: chunks,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}

func (s *Store) listEvents(sessionID int64) ([]Event, error) {
	rows, err := s.db.Query(`
SELECT id, session_id, ts, type, payload_json
FROM events
WHERE session_id = ?
ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var ts string
		var payload string
		if err := rows.Scan(&event.ID, &event.SessionID, &ts, &event.Type, &payload); err != nil {
			return nil, err
		}
		event.TS, err = parseTime(ts)
		if err != nil {
			return nil, err
		}
		event.Payload = json.RawMessage(payload)
		events = append(events, event)
	}
	return events, rows.Err()
}

type sessionScanner interface {
	Scan(dest ...any) error
}

func scanSession(scanner sessionScanner) (Session, error) {
	var session Session
	var argvJSON string
	var started string
	var ended sql.NullString
	var exit sql.NullInt64
	if err := scanner.Scan(
		&session.ID,
		&session.Agent,
		&argvJSON,
		&session.Cwd,
		&session.RepoRoot,
		&session.GitBranch,
		&started,
		&ended,
		&exit,
	); err != nil {
		return Session{}, err
	}
	if err := json.Unmarshal([]byte(argvJSON), &session.Argv); err != nil {
		return Session{}, err
	}
	var err error
	session.StartedAt, err = parseTime(started)
	if err != nil {
		return Session{}, err
	}
	if ended.Valid {
		t, err := parseTime(ended.String)
		if err != nil {
			return Session{}, err
		}
		session.EndedAt = &t
	}
	if exit.Valid {
		code := int(exit.Int64)
		session.ExitCode = &code
	}
	return session, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
