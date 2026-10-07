package byodserver

import (
	"net/http/httptest"
	"testing"
)

func TestRuntimeRoleRouting(t *testing.T) {
	for _, role := range []string{"control", "data"} {
		s, _ := NewService("https://exam.cs.ac.cn", "https://source.example", nil)
		s.Role = role
		for _, path := range []string{"/healthz", "/readyz", "/admin/", "/auth/login", "/v1/sessions", "/v1/xhttp/invalid"} {
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if path == "/healthz" || path == "/readyz" {
				if w.Code != 200 {
					t.Fatalf("%s %s: %d", role, path, w.Code)
				}
			} else if (role == "data" && path != "/v1/xhttp/invalid") || (role == "control" && path == "/v1/xhttp/invalid") {
				if w.Code != 404 {
					t.Fatalf("%s exposes %s: %d", role, path, w.Code)
				}
			} else if role == "data" && w.Code != 400 {
				t.Fatalf("data did not handle XHTTP: %d", w.Code)
			}
		}
	}
}
