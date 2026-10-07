package byodserver

import (
	"context"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

type splitFixture struct {
	control, data *Service
	session       *Session
	user          User
	uuid          string
}

func newSplitFixture(t *testing.T) *splitFixture {
	t.Helper()
	databaseURL := os.Getenv("BYOD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BYOD_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := MigratePostgres(ctx, databaseURL); err != nil {
		t.Fatal(err)
	}
	open := func(role string) *Service {
		s, _ := NewService("https://exam.cs.ac.cn", "https://source.example", nil)
		var err error
		s.ExamStore, err = OpenPostgresStore(ctx, databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.ExamStore.Close() })
		s.Role, s.IdentityIssuer = role, "https://split-idp.example"
		return s
	}
	f := &splitFixture{control: open("control"), data: open("data")}
	store := f.control.ExamStore
	exam, err := store.CreateExam(ctx, "split-"+randomToken(8), "https://source.example", nil, nil, map[string]any{"tunnel_hosts": []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetExamState(ctx, exam.ID, "active"); err != nil {
		t.Fatal(err)
	}
	f.user, err = store.ResolveIdentity(ctx, OIDCIdentity{Issuer: f.control.identityIssuer(), Subject: randomToken(12), Name: "Isolated split test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetParticipant(ctx, exam.ID, f.user.ID, true, f.user.ID); err != nil {
		t.Fatal(err)
	}
	f.session = &Session{ID: randomToken(18), ExamID: exam.ID, Subject: *f.user.Subject, State: "active", BrowserToken: randomToken(32), CreatedAt: time.Now().Unix(), LastSeenAt: time.Now().Add(-time.Minute).Unix()}
	f.control.sessions[f.session.ID] = f.session
	if err := store.SaveSession(ctx, f.session); err != nil {
		t.Fatal(err)
	}
	f.uuid, _, err = f.control.IssueTunnelTicket(ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPostgresSplitLivenessAndControlRestart(t *testing.T) {
	f := newSplitFixture(t)
	ctx := context.Background()
	if _, err := f.data.lookupVLESSCredential(ctx, f.uuid); err != nil {
		t.Fatal(err)
	}
	if len(f.data.sessions) != 0 {
		t.Fatal("data plane populated local control state")
	}
	if !f.control.enforceIdleTimeout(f.session) || f.session.State != "active" {
		t.Fatal("control suspended live data stream")
	}
	// Stale control writes cannot roll back a fresher data-plane heartbeat.
	stale := *f.session
	stale.LastSeenAt -= 60
	if err := f.control.ExamStore.SaveSession(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	stored, err := f.control.ExamStore.GetSession(ctx, f.session.ID)
	if err != nil || stored.LastSeenAt.Unix() < f.session.LastSeenAt {
		t.Fatalf("heartbeat regressed: %v", err)
	}
	if events, err := f.control.ExamStore.EndIdleSessions(ctx, f.session.ExamID, f.session.ID, time.Now(), 300); err != nil || len(events) != 0 {
		t.Fatalf("fresh heartbeat lost idle race: %v", err)
	}
	// A new control process restores the durable session; stopping the old
	// control's transport state must have no effect on the data plane.
	f.control.CloseTunnels()
	restarted, _ := NewService(f.control.ExamOrigin, "https://source.example", nil)
	restarted.ExamStore, restarted.IdentityIssuer, restarted.Role = f.control.ExamStore, f.control.IdentityIssuer, "control"
	if restarted.authorize(f.session.BrowserToken, f.session.ID) == nil {
		t.Fatal("control restart lost session")
	}
	if _, err := f.data.lookupVLESSCredential(ctx, f.uuid); err != nil {
		t.Fatal(err)
	}
	if err := f.control.ExamStore.RevokeTunnelTickets(ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.data.lookupVLESSCredential(ctx, f.uuid); err == nil {
		t.Fatal("revoked UUID survived control restart")
	}
}

func TestPostgresSplitRevokesEstablishedStreams(t *testing.T) {
	for _, reason := range []string{"credential", "suspend", "policy", "roster", "expiry", "window", "completion", "database", "session_timeout"} {
		t.Run(reason, func(t *testing.T) {
			f := newSplitFixture(t)
			ctx := context.Background()
			upstream, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer upstream.Close()
			go func() {
				c, e := upstream.Accept()
				if e == nil {
					defer c.Close()
					_, _ = io.Copy(c, c)
				}
			}()
			front := httptest.NewTLSServer(f.data)
			defer front.Close()
			defer f.data.CloseTunnels()
			conn := dialTestXHTTP(t, front)
			defer conn.Close()
			if _, err := conn.Write(append(vlessRequest(f.uuid, upstream.Addr().String()), []byte("echo")...)); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, 4)
			if _, err := io.ReadFull(conn, got); err != nil || string(got) != "echo" {
				t.Fatalf("independent data plane echo: %q %v", got, err)
			}
			f.control.CloseTunnels()
			if _, err := conn.Write([]byte("live")); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(conn, got); err != nil || string(got) != "live" {
				t.Fatal("control shutdown closed data stream", err)
			}
			store := f.control.ExamStore
			switch reason {
			case "credential":
				err = store.RevokeTunnelTickets(ctx, f.session.ID)
			case "suspend":
				f.session.State = "suspended"
				err = store.SaveSession(ctx, f.session)
			case "policy":
				_, err = store.db.ExecContext(ctx, `UPDATE byod_exams SET policy_json='{}' WHERE exam_id=$1`, f.session.ExamID)
			case "roster":
				err = store.SetParticipant(ctx, f.session.ExamID, f.user.ID, false, f.user.ID)
			case "expiry":
				_, err = store.db.ExecContext(ctx, `UPDATE byod_tunnel_tickets SET expires_at=now()-interval '1 second' WHERE session_id=$1`, f.session.ID)
			case "window":
				_, err = store.db.ExecContext(ctx, `UPDATE byod_exams SET ends_at=now()-interval '1 second' WHERE exam_id=$1`, f.session.ExamID)
			case "completion":
				_, err = store.RecordCompletion(ctx, f.session.ExamID, f.session.Subject, f.session.ID, time.Now())
			case "session_timeout":
				_, err = store.EndIdleSessions(ctx, f.session.ExamID, f.session.ID, time.Now().Add(301*time.Second), 300)
			case "database":
				f.data.ExamStore.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if _, err := conn.Read(got); err == nil {
				t.Fatal("authorization change did not close stream")
			}
			if time.Since(started) > 5*time.Second {
				t.Fatal("cross-process revocation too slow")
			}
			if reason == "suspend" {
				stored, err := store.GetSession(ctx, f.session.ID)
				if err != nil || stored.State != "suspended" {
					t.Fatal("data liveness resurrected session", err)
				}
			}
		})
	}
}

func TestPostgresSplitRejectsWrongIssuer(t *testing.T) {
	f := newSplitFixture(t)
	f.data.IdentityIssuer = "https://other-idp.example"
	if _, err := f.data.lookupVLESSCredential(context.Background(), f.uuid); err == nil {
		t.Fatal("wrong issuer accepted")
	}
}
