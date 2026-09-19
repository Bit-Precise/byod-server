package byodserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// Run with BYOD_TEST_DATABASE_URL against an isolated test database. The
// normal unit suite remains database-free, while this verifies the exact
// transaction boundaries used by a multi-replica deployment.
func TestPostgresExamLifecycle(t *testing.T) {
	databaseURL := os.Getenv("BYOD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BYOD_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := MigratePostgres(ctx, databaseURL); err != nil {
		t.Fatal(err)
	}
	store, err := OpenPostgresStore(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const examID = "integration-lifecycle"
	_ = store.DeleteExam(ctx, examID)
	if err := store.UpsertExamDetailsWithCode(ctx, examID, "ABC12345", "https://source.example", "active", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStudentDetails(ctx, examID, "oidc:student-42", "Student", true); err != nil {
		t.Fatal(err)
	}
	service, err := NewService("https://exam.cs.ac.cn", "https://source.example", []byte("integration-secret"))
	if err != nil {
		t.Fatal(err)
	}
	service.DevAuth = true
	service.ExamStore = store
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"integration-lifecycle","exam_code":"ABC12345"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var session map[string]string
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	callback := httptest.NewRecorder()
	service.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/oidc/callback?state="+session["session_id"]+"&code=student-42", nil))
	if callback.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", callback.Code, callback.Body.String())
	}
	start := httptest.NewRecorder()
	startRequest := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/start", nil)
	startRequest.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(start, startRequest)
	if start.Code != http.StatusOK {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	// A fresh Service has an empty in-memory session map. The browser token
	// must still authorize the durable session after a process restart.
	restarted, err := NewService("https://exam.cs.ac.cn", "https://source.example", []byte("integration-secret"))
	if err != nil {
		t.Fatal(err)
	}
	restarted.DevAuth = true
	restarted.ExamStore = store
	restored := httptest.NewRecorder()
	restoredRequest := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+session["session_id"], nil)
	restoredRequest.Header.Set("Authorization", "Bearer "+session["browser_token"])
	restarted.ServeHTTP(restored, restoredRequest)
	if restored.Code != http.StatusOK {
		t.Fatalf("restored session: %d %s", restored.Code, restored.Body.String())
	}
	complete := httptest.NewRecorder()
	completeRequest := httptest.NewRequest(http.MethodPost, "/v1/exams/"+examID+"/complete", nil)
	completeRequest.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(complete, completeRequest)
	if complete.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", complete.Code, complete.Body.String())
	}
	second := httptest.NewRecorder()
	service.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"integration-lifecycle","exam_code":"ABC12345"}`)))
	var secondSession map[string]string
	_ = json.Unmarshal(second.Body.Bytes(), &secondSession)
	secondCallback := httptest.NewRecorder()
	service.ServeHTTP(secondCallback, httptest.NewRequest(http.MethodGet, "/oidc/callback?state="+secondSession["session_id"]+"&code=student-42", nil))
	if secondCallback.Code != http.StatusGone {
		t.Fatalf("completed student re-entered: %d %s", secondCallback.Code, secondCallback.Body.String())
	}
}
