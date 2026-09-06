package byodserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConfigurationAndLifecycle(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service)
	defer server.Close()
	response, err := http.Get(server.URL + "/course-101/.well-known/byod-configuration")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	_ = json.NewDecoder(response.Body).Decode(&config)
	response.Body.Close()
	if config["version"] != float64(1) {
		t.Fatalf("unexpected config: %#v", config)
	}
	policy := config["policy"].(map[string]any)
	if policy["signature"] == "" {
		t.Fatal("policy is unsigned")
	}
	document := policy["document"].(map[string]any)
	navigation := document["navigation"].(map[string]any)
	allowedOrigins, ok := navigation["allowed_origins"].([]any)
	if !ok || len(allowedOrigins) != 2 || allowedOrigins[1].(string) != "http://127.0.0.1:9" {
		t.Fatalf("source origin is not allowed by navigation policy: %#v", navigation["allowed_origins"])
	}
	body := strings.NewReader(`{"exam_id":"course-101"}`)
	response, err = http.Post(server.URL+"/v1/sessions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]string
	_ = json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/sessions/"+created["session_id"]+"/start", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+created["browser_token"])
	response, _ = http.DefaultClient.Do(request)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("start before auth: %d", response.StatusCode)
	}
	response.Body.Close()
	_, _ = http.Get(server.URL + "/oidc/callback?state=" + created["session_id"] + "&code=student-42")
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/v1/sessions/"+created["session_id"]+"/start", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+created["browser_token"])
	response, _ = http.DefaultClient.Do(request)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("start after auth: %d", response.StatusCode)
	}
	response.Body.Close()
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/course-101/end", nil)
	request.Header.Set("Authorization", "Bearer "+created["browser_token"])
	response, _ = http.DefaultClient.Do(request)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unlock: %d", response.StatusCode)
	}
	response.Body.Close()
}

func TestAdminUIIsEmbedded(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "BYOD Server") {
		t.Fatalf("embedded admin UI unavailable: %d %q", recorder.Code, recorder.Body.String()[:min(120, len(recorder.Body.String()))])
	}
	index := recorder.Body.String()
	assetPathStart := strings.Index(index, "/admin/assets/")
	if assetPathStart < 0 {
		t.Fatal("admin index has no asset path")
	}
	assetPathEnd := strings.Index(index[assetPathStart:], "\"")
	asset := httptest.NewRecorder()
	service.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, index[assetPathStart:assetPathStart+assetPathEnd], nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("embedded admin asset unavailable: %d", asset.Code)
	}
}

func TestAdminRequiresToken(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	service.AdminToken = "correct-token"
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/api/exams", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", recorder.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestProxyInjectsIdentityAndBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]string{"subject": r.Header.Get("X-BYOD-Subject"), "session": r.Header.Get("X-BYOD-Session"), "body": string(body)})
	}))
	defer upstream.Close()
	service, _ := NewService("https://exam.cs.ac.cn", upstream.URL, []byte("test-secret"))
	server := httptest.NewServer(service)
	defer server.Close()
	response, _ := http.Post(server.URL+"/v1/sessions", "application/json", strings.NewReader(`{"exam_id":"course-101"}`))
	var created map[string]string
	_ = json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	noRedirect := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	callback, err := noRedirect.Get(server.URL + "/oidc/callback?state=" + created["session_id"] + "&code=student-42")
	if err != nil {
		t.Fatal(err)
	}
	callback.Body.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/sessions/"+created["session_id"]+"/start", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+created["browser_token"])
	response, _ = http.DefaultClient.Do(request)
	response.Body.Close()
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/course-101/answers", strings.NewReader(`{"answer":"42"}`))
	request.Header.Set("Authorization", "Bearer "+created["browser_token"])
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var proxied map[string]string
	_ = json.NewDecoder(response.Body).Decode(&proxied)
	if proxied["subject"] != "oidc:student-42" || proxied["body"] != `{"answer":"42"}` {
		t.Fatalf("proxy identity/body: %#v", proxied)
	}
}

func TestOIDCCallbackReturnsToGrips(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	server := httptest.NewServer(service)
	defer server.Close()
	response, err := http.Post(server.URL+"/v1/sessions", "application/json", strings.NewReader(`{"exam_id":"course-101","return_uri":"grips://exam/?target=https%3A%2F%2Fexam.cs.ac.cn%2Fcourse-101"}`))
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]string
	_ = json.NewDecoder(response.Body).Decode(&created)
	response.Body.Close()
	noRedirect := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	callback, err := noRedirect.Get(server.URL + "/oidc/callback?state=" + created["session_id"] + "&code=student-42")
	if err != nil {
		t.Fatal(err)
	}
	callback.Body.Close()
	if callback.StatusCode != http.StatusSeeOther || !strings.Contains(callback.Header.Get("Location"), "session_id=") {
		t.Fatalf("callback did not return to grips: %d %s", callback.StatusCode, callback.Header.Get("Location"))
	}
}

func TestDevelopmentAuthorizationAdapter(t *testing.T) {
	service, err := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	service.DevAuth = true
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"course-101"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d", create.Code)
	}
	var session map[string]string
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(session["authorization_url"], "https://exam.cs.ac.cn/dev/authorize?") {
		t.Fatalf("unexpected development authorization URL: %q", session["authorization_url"])
	}
	dev := httptest.NewRecorder()
	service.ServeHTTP(dev, httptest.NewRequest(http.MethodGet, "/dev/authorize?state="+session["session_id"], nil))
	if dev.Code != http.StatusSeeOther || !strings.Contains(dev.Header().Get("Location"), "/oidc/callback") {
		t.Fatalf("dev authorize: %d %s", dev.Code, dev.Header().Get("Location"))
	}
}

func TestOIDCCallbackStateIsSingleUse(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"course-101"}`)))
	var session map[string]string
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	results := make(chan int, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			callback := httptest.NewRecorder()
			service.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/oidc/callback?state="+session["session_id"]+"&code=student-42", nil))
			results <- callback.Code
		}()
	}
	wait.Wait()
	close(results)
	var success, rejected int
	for code := range results {
		if code == http.StatusOK {
			success++
		} else if code == http.StatusBadRequest {
			rejected++
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("callback state was not single-use: success=%d rejected=%d", success, rejected)
	}
}

func TestCORSPreflightForGrips(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/v1/sessions", nil)
	request.Header.Set("Origin", "grips://exam")
	request.Header.Set("Access-Control-Request-Headers", "authorization")
	service.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Origin") != "grips://exam" {
		t.Fatalf("unexpected CORS preflight response: %d %#v", recorder.Code, recorder.Header())
	}
}

func TestViolationSuspendsSession(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"course-101"}`)))
	var session map[string]string
	_ = json.Unmarshal(create.Body.Bytes(), &session)
	callback := httptest.NewRecorder()
	service.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/oidc/callback?state="+session["session_id"]+"&code=student-42", nil))
	start := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/start", strings.NewReader(`{}`))
	startReq.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(start, startReq)
	violation := httptest.NewRecorder()
	violationReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/violations", strings.NewReader(`{"type":"background"}`))
	violationReq.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(violation, violationReq)
	if violation.Code != http.StatusOK || !strings.Contains(violation.Body.String(), `"state":"suspended"`) {
		t.Fatalf("violation did not suspend session: %d %s", violation.Code, violation.Body.String())
	}
}

func TestHeartbeatUpdatesActiveSession(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"course-101"}`)))
	var session map[string]string
	_ = json.Unmarshal(create.Body.Bytes(), &session)
	callback := httptest.NewRecorder()
	service.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/oidc/callback?state="+session["session_id"]+"&code=student-42", nil))
	start := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/start", strings.NewReader(`{}`))
	startReq.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(start, startReq)
	heartbeat := httptest.NewRecorder()
	heartbeatReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/heartbeat", nil)
	heartbeatReq.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(heartbeat, heartbeatReq)
	if heartbeat.Code != http.StatusOK || !strings.Contains(heartbeat.Body.String(), `"state":"active"`) {
		t.Fatalf("heartbeat did not keep session active: %d %s", heartbeat.Code, heartbeat.Body.String())
	}
}

func TestPolicyOverrideParsing(t *testing.T) {
	overrides, err := ParsePolicyOverrides([]byte(`{"course-101":{"browser":{"allow_devtools":true}}}`))
	if err != nil || overrides["course-101"]["browser"] == nil {
		t.Fatalf("parse policy overrides: %#v %v", overrides, err)
	}
	single, err := ParsePolicyOverrides([]byte(`{"exam_id":"course-202","browser":{"allow_print":true}}`))
	if err != nil || single["course-202"] == nil {
		t.Fatalf("parse single policy: %#v %v", single, err)
	}
}

func TestPolicyOverrideMergesSafeDefaults(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	service.PolicyOverrides = map[string]map[string]any{"course-101": {"browser": map[string]any{"allow_print": true}}}
	document := service.policy("course-101")["document"].(map[string]any)
	browser := document["browser"].(map[string]any)
	if browser["allow_print"] != true || browser["allow_devtools"] != false || browser["kiosk_mode"] != true {
		t.Fatalf("policy defaults were not preserved: %#v", browser)
	}
}

func TestProxyRejectsPathOutsidePolicy(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	service.PolicyOverrides = map[string]map[string]any{"course-101": {"allowed_paths": []string{"/course-101/answers"}}}
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"course-101"}`)))
	var session map[string]string
	_ = json.Unmarshal(create.Body.Bytes(), &session)
	callback := httptest.NewRecorder()
	service.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/oidc/callback?state="+session["session_id"]+"&code=student-42", nil))
	start := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/start", strings.NewReader(`{}`))
	startReq.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(start, startReq)
	request := httptest.NewRequest(http.MethodGet, "/course-101/not-allowed", nil)
	request.Header.Set("Authorization", "Bearer "+session["browser_token"])
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "path_not_allowed") {
		t.Fatalf("unexpected policy response: %d %s", response.Code, response.Body.String())
	}
}

func TestProxyEnforcesIdleTimeout(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	service.PolicyOverrides = map[string]map[string]any{"course-101": {"session": map[string]any{"max_idle_seconds": 5}}}
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"course-101"}`)))
	var response map[string]string
	_ = json.Unmarshal(create.Body.Bytes(), &response)
	callback := httptest.NewRecorder()
	service.ServeHTTP(callback, httptest.NewRequest(http.MethodGet, "/oidc/callback?state="+response["session_id"]+"&code=student-42", nil))
	start := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+response["session_id"]+"/start", strings.NewReader(`{}`))
	startReq.Header.Set("Authorization", "Bearer "+response["browser_token"])
	service.ServeHTTP(start, startReq)
	service.mu.Lock()
	service.sessions[response["session_id"]].LastSeenAt = time.Now().Add(-time.Minute).Unix()
	service.mu.Unlock()
	request := httptest.NewRequest(http.MethodGet, "/course-101/answers", nil)
	request.Header.Set("Authorization", "Bearer "+response["browser_token"])
	result := httptest.NewRecorder()
	service.ServeHTTP(result, request)
	if result.Code != http.StatusForbidden || !strings.Contains(result.Body.String(), "session_suspended") {
		t.Fatalf("idle session was not suspended: %d %s", result.Code, result.Body.String())
	}
}

func TestPolicyAllowsExamRootAndDescendants(t *testing.T) {
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	if !service.pathAllowed("course-101", "/course-101") || !service.pathAllowed("course-101", "/course-101/answers") {
		t.Fatal("default exam path policy rejected valid root/descendant")
	}
	if service.pathAllowed("course-101", "/course-101-other") {
		t.Fatal("exam path policy accepted a prefix collision")
	}
}

func activateTestSession(t *testing.T, service *Service, examID string) map[string]string {
	t.Helper()
	create := httptest.NewRecorder()
	service.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"exam_id":"`+examID+`"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create session: %d %s", create.Code, create.Body.String())
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
	startRequest := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session["session_id"]+"/start", strings.NewReader(`{}`))
	startRequest.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(start, startRequest)
	if start.Code != http.StatusOK {
		t.Fatalf("start: %d %s", start.Code, start.Body.String())
	}
	return session
}

func TestProxyServesExamRootAndRejectsTraversal(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	service, _ := NewService("https://exam.cs.ac.cn", upstream.URL, []byte("test-secret"))
	session := activateTestSession(t, service, "course-101")

	root := httptest.NewRecorder()
	rootRequest := httptest.NewRequest(http.MethodGet, "/course-101", nil)
	rootRequest.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(root, rootRequest)
	if root.Code != http.StatusNoContent || len(paths) != 1 || paths[0] != "/" {
		t.Fatalf("exam root was not proxied: status=%d paths=%v", root.Code, paths)
	}

	traversal := httptest.NewRecorder()
	traversalRequest := httptest.NewRequest(http.MethodGet, "/course-101/../admin", nil)
	traversalRequest.Header.Set("Authorization", "Bearer "+session["browser_token"])
	service.ServeHTTP(traversal, traversalRequest)
	if traversal.Code != http.StatusForbidden || len(paths) != 1 {
		t.Fatalf("path traversal was not rejected: status=%d paths=%v", traversal.Code, paths)
	}
}

func TestExamUpstreamOverride(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	service, _ := NewService("https://exam.cs.ac.cn", "http://127.0.0.1:9", []byte("test-secret"))
	configured, err := ParseExamUpstreams([]byte(`{"course-101":"` + upstream.URL + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	service.ExamUpstreams = configured
	session := activateTestSession(t, service, "course-101")
	request := httptest.NewRequest(http.MethodGet, "/course-101/index.html", nil)
	request.Header.Set("Authorization", "Bearer "+session["browser_token"])
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || len(paths) != 1 || paths[0] != "/index.html" {
		t.Fatalf("exam-specific upstream was not used: status=%d paths=%v", response.Code, paths)
	}
}

func TestParseExamUpstreamsRejectsUnsafeURL(t *testing.T) {
	if _, err := ParseExamUpstreams([]byte(`{"course-101":"file:///etc/passwd"}`)); err == nil {
		t.Fatal("expected non-HTTP upstream to be rejected")
	}
	if _, err := ParseExamUpstreams([]byte(`{"../course":"https://example.test"}`)); err == nil {
		t.Fatal("expected unsafe exam ID to be rejected")
	}
}

func TestProxyHeadersRedirectAndIdentityIsolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "/next")
			w.Header().Set("X-Upstream", "present")
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "" || strings.Contains(r.Header.Get("Cookie"), "byod_session=") {
			http.Error(w, "identity credential leaked", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("X-Client") != "ok" {
			http.Error(w, "request headers were not forwarded", http.StatusInternalServerError)
			return
		}
		w.Header().Add("Set-Cookie", "exam_session=abc; Path=/")
		w.Header().Set("X-Upstream", "present")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	service, _ := NewService("https://exam.cs.ac.cn", upstream.URL, []byte("test-secret"))
	session := activateTestSession(t, service, "course-101")

	request := httptest.NewRequest(http.MethodGet, "/course-101/redirect", nil)
	request.Header.Set("Authorization", "Bearer "+session["browser_token"])
	request.Header.Set("Cookie", "byod_session="+session["browser_token"]+"; exam_session=old")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Client", "ok")
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/next" || response.Header().Get("X-Upstream") != "present" {
		t.Fatalf("upstream redirect/headers were not preserved: status=%d headers=%v", response.Code, response.Header())
	}

	request = httptest.NewRequest(http.MethodGet, "/course-101/next", nil)
	request.Header.Set("Authorization", "Bearer "+session["browser_token"])
	request.Header.Set("Cookie", "byod_session="+session["browser_token"]+"; exam_session=old")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Client", "ok")
	response = httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Set-Cookie") == "" {
		t.Fatalf("upstream response headers were not copied: status=%d headers=%v", response.Code, response.Header())
	}
}
