package memory

import (
	"path/filepath"
	"testing"
)

// openTestDB returns a fresh historyStore backed by a temp-dir SQLite file.
// Using a file (not :memory:) avoids URI quirks with modernc.org/sqlite.
func openTestDB(t *testing.T) *historyStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite3")
	h, err := newHistoryStore(path)
	if err != nil {
		t.Fatalf("newHistoryStore: %v", err)
	}
	t.Cleanup(func() { _ = h.close() })
	return h
}

func TestCreateSession_ReturnsPositiveID(t *testing.T) {
	h := openTestDB(t)
	id, err := h.createSession()
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive session ID, got %d", id)
	}
}

func TestCreateSession_IDsAreMonotonicallyIncreasing(t *testing.T) {
	h := openTestDB(t)
	id1, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	if id2 <= id1 {
		t.Errorf("expected id2 (%d) > id1 (%d)", id2, id1)
	}
}

func TestListSessions_EmptyDB(t *testing.T) {
	h := openTestDB(t)
	sessions, err := h.listSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestListSessions_ReturnsCreatedSessions(t *testing.T) {
	h := openTestDB(t)
	for i := 0; i < 3; i++ {
		if _, err := h.createSession(); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := h.listSessions(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}
}

func TestListSessions_LimitIsRespected(t *testing.T) {
	h := openTestDB(t)
	for i := 0; i < 5; i++ {
		if _, err := h.createSession(); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := h.listSessions(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions (limit), got %d", len(sessions))
	}
}

func TestAddMessage_StringContent(t *testing.T) {
	h := openTestDB(t)
	sid, _ := h.createSession()

	if err := h.addMessage(sid, "user", "hello world"); err != nil {
		t.Fatalf("addMessage: %v", err)
	}

	msgs, err := h.loadTail(sid, 10)
	if err != nil {
		t.Fatalf("loadTail: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role=user, got %q", msgs[0].Role)
	}
	if msgs[0].Content != "hello world" {
		t.Errorf("expected content 'hello world', got %q", msgs[0].Content)
	}
}

func TestAddMessage_StructContent(t *testing.T) {
	h := openTestDB(t)
	sid, _ := h.createSession()

	payload := map[string]any{"key": "value", "n": 42}
	if err := h.addMessage(sid, "tool", payload); err != nil {
		t.Fatalf("addMessage struct: %v", err)
	}

	msgs, err := h.loadTail(sid, 10)
	if err != nil {
		t.Fatalf("loadTail: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Struct payloads are kept as JSON; the message content should be non-empty.
	if msgs[0].Content == "" {
		t.Error("expected non-empty content for struct payload")
	}
}

func TestLoadTail_LimitIsRespected(t *testing.T) {
	h := openTestDB(t)
	sid, _ := h.createSession()

	for i := 0; i < 10; i++ {
		if err := h.addMessage(sid, "user", "msg"); err != nil {
			t.Fatal(err)
		}
	}

	msgs, err := h.loadTail(sid, 3)
	if err != nil {
		t.Fatal(err)
	}
	// loadTail returns the first N by ts ASC — always exactly limit rows (when
	// the table has enough rows).
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages (limit), got %d", len(msgs))
	}
}

func TestLoadTail_IsolatedBySessions(t *testing.T) {
	h := openTestDB(t)
	sid1, _ := h.createSession()
	sid2, _ := h.createSession()

	_ = h.addMessage(sid1, "user", "session1 message")
	_ = h.addMessage(sid2, "user", "session2 message")

	msgs1, _ := h.loadTail(sid1, 10)
	msgs2, _ := h.loadTail(sid2, 10)

	if len(msgs1) != 1 || msgs1[0].Content != "session1 message" {
		t.Errorf("session 1 messages wrong: %v", msgs1)
	}
	if len(msgs2) != 1 || msgs2[0].Content != "session2 message" {
		t.Errorf("session 2 messages wrong: %v", msgs2)
	}
}

func TestListMessages_PaginationOffset(t *testing.T) {
	h := openTestDB(t)
	sid, _ := h.createSession()

	for i := 0; i < 5; i++ {
		if err := h.addMessage(sid, "user", "msg"); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := h.listMessages(sid, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	page2, err := h.listMessages(sid, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 3 {
		t.Errorf("page1: expected 3, got %d", len(page1))
	}
	if len(page2) != 2 {
		t.Errorf("page2: expected 2, got %d", len(page2))
	}
}

func TestRecordAndGetInvocation(t *testing.T) {
	h := openTestDB(t)
	sid, _ := h.createSession()

	rec := AuditRecord{
		SessionID:  sid,
		Step:       1,
		Skill:      "my_skill",
		Kind:       "skill",
		Args:       map[string]any{"param": "test"},
		Result:     map[string]any{"ok": true},
		DurationMS: 42,
		Why:        "because testing",
	}
	if err := h.recordInvocation(rec, 65536); err != nil {
		t.Fatalf("recordInvocation: %v", err)
	}

	// list it back
	recs, total, err := h.listInvocations(AuditFilter{SessionID: sid, Limit: 10})
	if err != nil {
		t.Fatalf("listInvocations: %v", err)
	}
	if total != 1 {
		t.Errorf("expected total=1, got %d", total)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	got := recs[0]
	if got.Skill != "my_skill" {
		t.Errorf("skill mismatch: %q", got.Skill)
	}
	if got.DurationMS != 42 {
		t.Errorf("duration mismatch: %d", got.DurationMS)
	}
	if got.Why != "because testing" {
		t.Errorf("why mismatch: %q", got.Why)
	}

	// get by ID
	byID, err := h.getInvocation(got.ID)
	if err != nil {
		t.Fatalf("getInvocation: %v", err)
	}
	if byID == nil {
		t.Fatal("getInvocation returned nil")
	}
	if byID.Skill != "my_skill" {
		t.Errorf("getInvocation skill mismatch: %q", byID.Skill)
	}
}

func TestGetInvocation_NotFound(t *testing.T) {
	h := openTestDB(t)
	rec, err := h.getInvocation(999999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec != nil {
		t.Errorf("expected nil for missing ID, got %v", rec)
	}
}

func TestListInvocations_OnlyErrors(t *testing.T) {
	h := openTestDB(t)
	sid, _ := h.createSession()

	good := AuditRecord{SessionID: sid, Step: 1, Skill: "ok_skill", Kind: "skill", DurationMS: 1}
	bad := AuditRecord{SessionID: sid, Step: 2, Skill: "err_skill", Kind: "skill", DurationMS: 1, ErrorMsg: "something went wrong"}

	_ = h.recordInvocation(good, 65536)
	_ = h.recordInvocation(bad, 65536)

	recs, total, err := h.listInvocations(AuditFilter{OnlyErrors: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("expected 1 error record, got total=%d", total)
	}
	if len(recs) != 1 || recs[0].Skill != "err_skill" {
		t.Errorf("unexpected records: %v", recs)
	}
}

func TestListInvocations_FilterBySkill(t *testing.T) {
	h := openTestDB(t)
	sid, _ := h.createSession()

	_ = h.recordInvocation(AuditRecord{SessionID: sid, Skill: "alpha", Kind: "skill", DurationMS: 1}, 65536)
	_ = h.recordInvocation(AuditRecord{SessionID: sid, Skill: "beta", Kind: "skill", DurationMS: 1}, 65536)
	_ = h.recordInvocation(AuditRecord{SessionID: sid, Skill: "alpha", Kind: "skill", DurationMS: 1}, 65536)

	recs, total, err := h.listInvocations(AuditFilter{Skill: "alpha", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("expected 2 records for 'alpha', got %d", total)
	}
	_ = recs
}
