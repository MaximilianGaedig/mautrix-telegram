package connector

import (
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/presence"
)

func TestMapTelegramStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	future := int(now.Add(5 * time.Minute).Unix())
	past := int(now.Add(-time.Second).Unix())
	cases := []struct {
		name   string
		in     tg.UserStatusClass
		want   event.Presence
		until  time.Time
		mapped bool
		msg    string
	}{
		{"online", &tg.UserStatusOnline{Expires: future}, event.PresenceOnline, time.Unix(int64(future), 0), true, ""},
		{"online expired", &tg.UserStatusOnline{Expires: past}, event.PresenceUnavailable, time.Time{}, true, ""},
		{"online no expiry", &tg.UserStatusOnline{}, event.PresenceOnline, time.Time{}, true, ""},
		{"offline", &tg.UserStatusOffline{WasOnline: past}, event.PresenceOffline, time.Time{}, true, "last seen " + time.Unix(int64(past), 0).UTC().Format(time.RFC3339)},
		{"recently", &tg.UserStatusRecently{}, event.PresenceUnavailable, time.Time{}, true, "last seen recently"},
		{"last week", &tg.UserStatusLastWeek{}, event.PresenceUnavailable, time.Time{}, true, "last seen within a week"},
		{"last month", &tg.UserStatusLastMonth{}, event.PresenceUnavailable, time.Time{}, true, "last seen within a month"},
		{"empty", &tg.UserStatusEmpty{}, event.PresenceOffline, time.Time{}, true, ""},
		{"nil", nil, "", time.Time{}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, ok := mapTelegramStatus(c.in, now)
			if ok != c.mapped || st.Presence != c.want || !st.Until.Equal(c.until) || st.StatusMsg != c.msg {
				t.Fatalf("got (%+v, %v), want (%s until %v, %v)", st, ok, c.want, c.until, c.mapped)
			}
		})
	}
}

func TestLastOnlineTracker(t *testing.T) {
	var tr lastOnlineTracker
	t0 := time.Date(2026, 9, 18, 19, 40, 0, 0, time.UTC)
	recently := presence.State{Presence: event.PresenceUnavailable, StatusMsg: "last seen recently"}
	if got := tr.apply(1, recently, t0); got.StatusMsg != "last seen recently" {
		t.Fatalf("no observation yet, got %q", got.StatusMsg)
	}
	tr.apply(1, presence.State{Presence: event.PresenceOnline}, t0)
	if got := tr.apply(1, recently, t0.Add(10*time.Minute)); got.StatusMsg != "last seen 2026-09-18T19:40:00Z" {
		t.Fatalf("expected last observed online time, got %q", got.StatusMsg)
	}
	exact := presence.State{Presence: event.PresenceOffline, StatusMsg: "last seen 2026-09-18T19:45:00Z"}
	if got := tr.apply(1, exact, t0.Add(20*time.Minute)); got.StatusMsg != exact.StatusMsg {
		t.Fatalf("exact time from Telegram must win, got %q", got.StatusMsg)
	}
	if got := tr.apply(2, recently, t0); got.StatusMsg != "last seen recently" {
		t.Fatalf("other users unaffected, got %q", got.StatusMsg)
	}
}

func TestLastOnlineSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	saved := map[int64]time.Time{}
	var mu sync.Mutex
	persist := func(id int64, at time.Time) {
		mu.Lock()
		saved[id] = at
		mu.Unlock()
	}
	recently := presence.State{Presence: event.PresenceUnavailable, StatusMsg: LastSeenPrefix + "recently"}
	online := presence.State{Presence: event.PresenceOnline}

	before := &lastOnlineTracker{persist: persist}
	before.apply(42, online, now)
	// A refresh within the minute isn't written again.
	before.apply(42, online, now.Add(30*time.Second))
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return saved[42].Equal(now) })

	// After a restart the memory is empty; the saved time dates "recently".
	after := &lastOnlineTracker{load: func(id int64) time.Time { mu.Lock(); defer mu.Unlock(); return saved[id] }}
	got := after.apply(42, recently, now.Add(2*time.Hour))
	if want := LastSeenPrefix + "2026-09-19T10:00:00Z"; got.StatusMsg != want {
		t.Fatalf("status %q, want %q", got.StatusMsg, want)
	}
	// Without a saved time it stays vague.
	if got = after.apply(7, recently, now); got.StatusMsg != LastSeenPrefix+"recently" {
		t.Fatalf("unknown user got %q", got.StatusMsg)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}
