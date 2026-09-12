package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
)

// fixedSchedule builds a scheduler pinned to a fixed wall clock so due-ness is
// deterministic.
func fixedSchedule(t0 time.Time) *Schedule {
	s := NewSchedule()
	s.Now = func() time.Time { return t0 }
	return s
}

// execCreate runs schedule_create with raw JSON arguments and returns the
// result text and error.
func execCreate(t *testing.T, s *Schedule, args string) (string, error) {
	t.Helper()
	res, err := (&scheduleCreateTool{sched: s}).Execute(context.Background(), "call-1", json.RawMessage(args), nil)
	if err != nil {
		return "", err
	}
	return agentcore.ContentToText(res.Content), nil
}

// TestScheduleCreateParseTargets exercises the strict time-parsing rules of
// schedule_create: RFC 3339 with Z or numeric offset, the date/time/time_zone
// triple against UTC or IANA zones, DST gap rejection, fall-back overlap
// resolution to the first instant, and the interval floor (issue #565).
func TestScheduleCreateParseTargets(t *testing.T) {
	t0 := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	s := fixedSchedule(t0)

	cases := []struct {
		name    string
		args    string
		wantErr string
	}{
		{name: "at with Z", args: `{"prompt":"p","at":"2026-09-12T12:00:00Z"}`},
		{name: "at with numeric offset", args: `{"prompt":"p","at":"2026-09-12T20:00:00+08:00"}`},
		{name: "triple with IANA zone", args: `{"prompt":"p","date":"2026-09-12","time":"20:00:00","time_zone":"Asia/Shanghai"}`},
		{name: "triple with UTC", args: `{"prompt":"p","date":"2026-09-12","time":"12:00","time_zone":"UTC"}`},
		{name: "interval at floor", args: `{"prompt":"p","every_seconds":300}`},
		{name: "at without zone", args: `{"prompt":"p","at":"2026-09-12T20:00:00"}`, wantErr: "RFC 3339"},
		{name: "at garbage", args: `{"prompt":"p","at":"tomorrow"}`, wantErr: "RFC 3339"},
		{name: "triple with Local", args: `{"prompt":"p","date":"2026-09-12","time":"12:00:00","time_zone":"Local"}`, wantErr: "IANA"},
		{name: "triple with unknown zone", args: `{"prompt":"p","date":"2026-09-12","time":"12:00:00","time_zone":"Mars/Olympus"}`, wantErr: "IANA"},
		{name: "triple missing time", args: `{"prompt":"p","date":"2026-09-12","time_zone":"UTC"}`, wantErr: "together"},
		{name: "dst spring-forward gap", args: `{"prompt":"p","date":"2026-03-08","time":"02:30:00","time_zone":"America/New_York"}`, wantErr: "DST gap"},
		{name: "dst fall-back overlap resolves", args: `{"prompt":"p","date":"2026-11-01","time":"01:30:00","time_zone":"America/New_York"}`},
		{name: "interval below floor", args: `{"prompt":"p","every_seconds":299}`, wantErr: "every_seconds"},
		{name: "interval negative", args: `{"prompt":"p","every_seconds":-5}`, wantErr: "positive"},
		{name: "at and interval mixed", args: `{"prompt":"p","at":"2026-09-12T12:00:00Z","every_seconds":300}`, wantErr: "exactly one"},
		{name: "no target", args: `{"prompt":"p"}`, wantErr: "required"},
		{name: "blank prompt", args: `{"prompt":"  ","at":"2026-09-12T12:00:00Z"}`, wantErr: "prompt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, err := execCreate(t, s, tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !strings.Contains(text, "session-local") {
				t.Errorf("result = %q, want it to mark session-local delivery", text)
			}
		})
	}

	// The fall-back overlap (2026-11-01 01:30 America/New_York) must resolve to
	// the first instant, which is EDT (UTC-4) => 05:30Z.
	s2 := fixedSchedule(t0)
	if _, err := execCreate(t, s2, `{"prompt":"overlap","date":"2026-11-01","time":"01:30:00","time_zone":"America/New_York"}`); err != nil {
		t.Fatalf("overlap create: %v", err)
	}
	got := s2.List()[0].At.UTC()
	if want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("overlap At = %s, want %s (first instant, EDT)", got, want)
	}
}

// TestScheduleCreateListDeleteFlow drives the three tools end to end over the
// store: create (one-shot and repeating), list with status, delete, and the
// unknown-id error.
func TestScheduleCreateListDeleteFlow(t *testing.T) {
	t0 := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	s := fixedSchedule(t0)
	list := &scheduleListTool{sched: s}
	del := &scheduleDeleteTool{sched: s}

	if _, err := execCreate(t, s, `{"prompt":"check deploy","at":"2026-09-12T13:00:00Z"}`); err != nil {
		t.Fatalf("create one-shot: %v", err)
	}
	if _, err := execCreate(t, s, `{"prompt":"standup","every_seconds":900}`); err != nil {
		t.Fatalf("create repeating: %v", err)
	}

	text, err := list.Execute(context.Background(), "call-2", json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := agentcore.ContentToText(text.Content)
	for _, want := range []string{"sched-1", "scheduled", "one-shot", "sched-2", "repeating", "every 15m0s", "2026-09-12T12:15:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}

	// Advance the clock past the one-shot target: it is overdue, then delivered.
	s.Now = func() time.Time { return t0.Add(2 * time.Hour) }
	text, _ = list.Execute(context.Background(), "call-3", json.RawMessage(`{}`), nil)
	if out = agentcore.ContentToText(text.Content); !strings.Contains(out, "overdue") {
		t.Errorf("list output should show overdue one-shot:\n%s", out)
	}

	// Every list line carries its own prompt, so the model can see what each
	// reminder is for (review finding: prompts must not be list-truncated).
	if !strings.Contains(out, "prompt: check deploy") || !strings.Contains(out, "prompt: standup") {
		t.Errorf("list output should carry each reminder's prompt:\n%s", out)
	}

	if _, err := execCreate(t, s, `{"prompt":"x","at":"2026-09-12T13:00:00Z"}`); err != nil {
		t.Fatalf("re-create: %v", err)
	}
	if !s.Delete("sched-1") {
		t.Fatal("Delete(sched-1) = false, want true")
	}
	if s.Delete("sched-1") {
		t.Fatal("double Delete(sched-1) = true, want false")
	}
	if _, err := del.Execute(context.Background(), "call-5", json.RawMessage(`{"id":"sched-999"}`), nil); err == nil {
		t.Fatal("delete unknown id succeeded, want error")
	}
}

// TestScheduleFollowUpDelivery verifies the delivery seam: one-shot reminders
// fire exactly once when due, repeating reminders re-arm on their interval,
// and multiple due reminders are returned in creation order (issue #565).
func TestScheduleFollowUpDelivery(t *testing.T) {
	t0 := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	s := fixedSchedule(t0)
	now := t0

	if _, err := s.Create("past due", &t0, 0); err != nil {
		t.Fatalf("create overdue: %v", err)
	}
	future := t0.Add(time.Hour)
	if _, err := s.Create("not yet", &future, 0); err != nil {
		t.Fatalf("create future: %v", err)
	}
	if _, err := s.Create("every five", nil, 5*time.Minute); err != nil {
		t.Fatalf("create repeating: %v", err)
	}

	// t0: only the overdue one-shot is due.
	msgs := s.FollowUpMessages(context.Background(), nil)
	if len(msgs) != 1 {
		t.Fatalf("delivery at t0 = %+v, want exactly the overdue one-shot", msgs)
	}
	if u, ok := msgs[0].(agentcore.UserMessage); !ok || agentcore.ContentToText(u.Content) != "past due" {
		t.Fatalf("delivery at t0 = %T %+v, want the overdue one-shot prompt", msgs[0], msgs[0])
	}

	// t0+5m: the repeating reminder is due; the delivered one-shot is not re-sent.
	now = t0.Add(5 * time.Minute)
	s.Now = func() time.Time { return now }
	msgs = s.FollowUpMessages(context.Background(), nil)
	if len(msgs) != 1 {
		t.Fatalf("delivery at t0+5m = %+v, want exactly the repeating reminder", msgs)
	}
	if u, ok := msgs[0].(agentcore.UserMessage); !ok || agentcore.ContentToText(u.Content) != "every five" {
		t.Fatalf("delivery at t0+5m = %T %+v, want the repeating prompt", msgs[0], msgs[0])
	}

	// t0+9m: interval has not elapsed again; nothing due.
	now = t0.Add(9 * time.Minute)
	msgs = s.FollowUpMessages(context.Background(), nil)
	if len(msgs) != 0 {
		t.Fatalf("delivery at t0+9m = %+v, want none", msgs)
	}

	// t0+10m: repeating fires again.
	now = t0.Add(10 * time.Minute)
	msgs = s.FollowUpMessages(context.Background(), nil)
	if len(msgs) != 1 {
		t.Fatalf("delivery at t0+10m = %+v, want the repeating reminder", msgs)
	}

	// The future one-shot never fired; the delivered one shows as delivered.
	listed := s.List()
	if listed[0].Status(now) != "delivered" {
		t.Errorf("one-shot status = %q, want delivered", listed[0].Status(now))
	}
	if listed[1].Status(now) != "scheduled" {
		t.Errorf("future one-shot status = %q, want scheduled", listed[1].Status(now))
	}
}

// TestScheduleConcurrentAccess hammers create/consult/delete from many
// goroutines to prove the store is race-free under -race (the executor may run
// schedule_* calls in a parallel batch while the loop delivers).
func TestScheduleConcurrentAccess(t *testing.T) {
	s := NewSchedule()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				at := time.Now().UTC()
				_, _ = s.Create("concurrent", &at, 0)
				_ = s.FollowUpMessages(context.Background(), nil)
				_ = s.List()
			}
		}()
	}
	wg.Wait()
}
