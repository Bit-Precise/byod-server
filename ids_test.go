package byodserver

import "testing"

func TestNewExamUUID(t *testing.T) {
	first, err := newExamUUID()
	if err != nil || !validExamUUID(first) {
		t.Fatalf("generated invalid exam UUID %q: %v", first, err)
	}
	second, err := newExamUUID()
	if err != nil || first == second {
		t.Fatalf("generated duplicate exam UUIDs: %q", first)
	}
}

func TestExamHashtagValidation(t *testing.T) {
	for _, value := range []string{"course-101", "midterm_2026", "cs101.exam"} {
		if !validExamHashtag(value) {
			t.Errorf("hashtag %q should be accepted", value)
		}
	}
	for _, value := range []string{"", "#course-101", "course/101", "course 101"} {
		if validExamHashtag(value) {
			t.Errorf("hashtag %q should be rejected", value)
		}
	}
}

func TestExamNameValidation(t *testing.T) {
	for _, value := range []string{"期末考试", "Computer Science 101 Final"} {
		if !validExamName(value) {
			t.Errorf("exam name %q should be accepted", value)
		}
	}
	for _, value := range []string{"", "   "} {
		if validExamName(value) {
			t.Errorf("exam name %q should be rejected", value)
		}
	}
}
