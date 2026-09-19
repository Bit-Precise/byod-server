package byodserver

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var (
	ErrExamNotFound      = errors.New("exam_not_found")
	ErrExamNotPublished  = errors.New("exam_not_published")
	ErrExamNotStarted    = errors.New("exam_not_started")
	ErrExamEnded         = errors.New("exam_ended")
	ErrExamAlreadyDone   = errors.New("exam_already_completed")
	ErrExamStudentDenied = errors.New("student_not_allowed")
	ErrExamInvalidWindow = errors.New("invalid_exam_window")
)

const examCodeAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// validExamCode is deliberately stricter than an exam ID. Codes are short,
// case-insensitive human entry values and are always represented in upper
// case at the API boundary.
func validExamCode(code string) bool {
	if len(code) != 8 {
		return false
	}
	for _, c := range strings.ToUpper(code) {
		if !strings.ContainsRune(examCodeAlphabet, c) {
			return false
		}
	}
	return true
}

func normalizeExamCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func newExamCode() (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	var out strings.Builder
	out.Grow(len(buffer))
	for _, b := range buffer {
		out.WriteByte(examCodeAlphabet[int(b)%len(examCodeAlphabet)])
	}
	return out.String(), nil
}

type examGate int

const (
	gateAuthenticate examGate = iota
	gateStart
	gateActive
)

// examWindow is the one server-side source of truth for publication and
// timing. A scheduled exam may authenticate before starts_at, but cannot be
// started until the opening time. Any request after ends_at is terminal.
func (s *Service) examWindow(ctx context.Context, examID string, gate examGate) (StoredExam, error) {
	var exam StoredExam
	if s.ExamStore == nil {
		// The no-database mode is used by local protocol tests. It represents an
		// already-published exam with no time bounds.
		exam = StoredExam{ID: examID, State: "active"}
	} else {
		loaded, ok, err := s.ExamStore.GetExam(ctx, examID)
		if err != nil {
			return StoredExam{}, err
		}
		if !ok {
			return StoredExam{}, ErrExamNotFound
		}
		exam = *loaded
	}

	now := time.Now()
	if exam.State == "ended" || (exam.EndsAt != nil && !now.Before(*exam.EndsAt)) {
		if s.ExamStore != nil {
			_ = s.ExamStore.MarkExamEnded(ctx, exam.ID, now)
			_ = s.expireExamSessions(ctx, exam.ID)
		}
		return exam, ErrExamEnded
	}
	if exam.State == "draft" || exam.State == "" {
		return exam, ErrExamNotPublished
	}
	if exam.State == "scheduled" && exam.StartsAt != nil && !now.Before(*exam.StartsAt) {
		exam.State = "active"
		if s.ExamStore != nil {
			_ = s.ExamStore.SetExamState(ctx, exam.ID, "active")
		}
	}
	if gate == gateStart || gate == gateActive {
		if exam.StartsAt != nil && now.Before(*exam.StartsAt) {
			return exam, ErrExamNotStarted
		}
	}
	return exam, nil
}

// expireExamSessions mirrors the terminal transition in memory after the DB
// bulk update. It is called from every time-gated path, so an exam does not
// require a separate scheduler to stop forwarding traffic.
func (s *Service) expireExamSessions(ctx context.Context, examID string) error {
	if s.ExamStore == nil {
		return nil
	}
	sessions, err := s.ExamStore.ExpireSessions(ctx, examID, time.Now())
	if err != nil {
		return err
	}
	for _, stored := range sessions {
		s.mu.Lock()
		live := s.sessions[stored.ID]
		if live != nil && live.State != "ended" {
			live.State = "ended"
			live.LastSeenAt = stored.LastSeenAt.Unix()
			if s.events == nil {
				s.events = make(map[string][]ExamEvent)
			}
			event := ExamEvent{ID: randomToken(12), SessionID: live.ID, AttemptID: live.AttemptID, BrowserSessionID: live.BrowserSessionID, Type: "exam_ended", Severity: "info", Details: "ends_at", OccurredAt: time.Now().Unix()}
			s.events[live.ID] = append(s.events[live.ID], event)
			s.mu.Unlock()
			_ = s.ExamStore.SaveEvent(ctx, event)
		} else {
			s.mu.Unlock()
		}
	}
	return nil
}

func (s *Service) examForCode(ctx context.Context, code string) (*StoredExam, error) {
	code = normalizeExamCode(code)
	if !validExamCode(code) {
		return nil, fmt.Errorf("invalid_exam_code")
	}
	if s.ExamStore != nil {
		exam, ok, err := s.ExamStore.ExamByCode(ctx, code)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrExamNotFound
		}
		return exam, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, exam := range s.exams {
		if exam != nil && exam.ExamCode == code {
			return &StoredExam{ID: exam.ID, ExamCode: exam.ExamCode, State: "active"}, nil
		}
	}
	return nil, ErrExamNotFound
}

func (s *Service) studentCompleted(ctx context.Context, examID, subject string) (bool, error) {
	if subject == "" {
		return false, nil
	}
	if s.ExamStore != nil {
		return s.ExamStore.StudentCompleted(ctx, examID, subject)
	}
	s.mu.RLock()
	_, ok := s.completions[examID+"\x00"+subject]
	s.mu.RUnlock()
	return ok, nil
}

// recordStudentCompletion atomically claims the one completion slot for a
// student. The boolean is false when another session already claimed it; a
// caller must not treat that race as a successful second submission.
func (s *Service) recordStudentCompletion(ctx context.Context, examID, subject, sessionID string, completedAt time.Time) (bool, error) {
	if subject == "" {
		return true, nil
	}
	if s.ExamStore != nil {
		return s.ExamStore.RecordCompletion(ctx, examID, subject, sessionID, completedAt)
	}
	s.mu.Lock()
	if s.completions == nil {
		s.completions = make(map[string]time.Time)
	}
	key := examID + "\x00" + subject
	if _, exists := s.completions[key]; exists {
		s.mu.Unlock()
		return false, nil
	}
	s.completions[key] = completedAt
	s.mu.Unlock()
	return true, nil
}

func examErrorStatus(err error) int {
	switch {
	case errors.Is(err, ErrExamNotFound):
		return 404
	case errors.Is(err, ErrExamNotPublished):
		return 409
	case errors.Is(err, ErrExamNotStarted):
		return 409
	case errors.Is(err, ErrExamEnded), errors.Is(err, ErrExamAlreadyDone):
		return 410
	case errors.Is(err, ErrExamStudentDenied):
		return 403
	default:
		return 503
	}
}

func (s *Service) writeExamError(w http.ResponseWriter, err error) {
	status := examErrorStatus(err)
	s.writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Service) writeCompletionError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrExamAlreadyDone) || errors.Is(err, ErrExamEnded) {
		s.writeJSON(w, http.StatusGone, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "completion_persist_failed"})
}
