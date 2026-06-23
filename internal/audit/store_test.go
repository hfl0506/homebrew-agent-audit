package audit

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreExportSessionJSON(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "audit.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sessionID, err := store.CreateSession(Session{
		Agent: "echo",
		Argv:  []string{"/bin/echo", "hello"},
		Cwd:   "/tmp",
		StartedAt: time.Date(
			2026, 6, 23, 12, 0, 0, 0, time.UTC,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertEvent(sessionID, "process.start", map[string]any{"cwd": "/tmp"}); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertTranscript(sessionID, "pty", []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSession(sessionID, time.Now(), 0); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := store.ExportSessionJSON(&out, sessionID); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Session    Session           `json:"session"`
		Events     []Event           `json:"events"`
		Transcript []TranscriptChunk `json:"transcript"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Session.Agent != "echo" {
		t.Fatalf("agent = %q, want echo", payload.Session.Agent)
	}
	if len(payload.Events) != 1 {
		t.Fatalf("events length = %d, want 1", len(payload.Events))
	}
	if !json.Valid(payload.Events[0].Payload) {
		t.Fatalf("event payload is invalid json: %s", payload.Events[0].Payload)
	}
	if len(payload.Transcript) != 1 || payload.Transcript[0].Content != "hello\n" {
		t.Fatalf("unexpected transcript: %#v", payload.Transcript)
	}
}
