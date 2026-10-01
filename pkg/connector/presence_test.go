package connector

import (
	"slices"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
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
		{"online expired", &tg.UserStatusOnline{Expires: past}, event.PresenceOffline, time.Time{}, true, ""},
		{"online no expiry", &tg.UserStatusOnline{}, event.PresenceOnline, time.Time{}, true, ""},
		{"offline", &tg.UserStatusOffline{WasOnline: past}, event.PresenceOffline, time.Time{}, true, ""},
		// Hidden last seen says nothing about now: ignored (activity covers these users).
		{"recently", &tg.UserStatusRecently{}, "", time.Time{}, false, ""},
		{"last week", &tg.UserStatusLastWeek{}, "", time.Time{}, false, ""},
		{"last month", &tg.UserStatusLastMonth{}, "", time.Time{}, false, ""},
		{"empty", &tg.UserStatusEmpty{}, "", time.Time{}, false, ""},
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

// Group members were never asked about: only contacts and DM partners were, so a group's members
// showed as offline unless they wrote something.
func TestMemberRotation(t *testing.T) {
	var r memberRotation
	if got := r.batch(100); len(got) != 0 {
		t.Fatalf("nobody to ask about, got %v", got)
	}
	r.set([]int64{1, 2, 3, 4, 5})
	if got := r.batch(2); !slices.Equal(got, []int64{1, 2}) {
		t.Fatalf("first batch: %v", got)
	}
	if got := r.batch(2); !slices.Equal(got, []int64{3, 4}) {
		t.Fatalf("second batch: %v", got)
	}
	// The round wraps, so everyone is asked about again and again.
	if got := r.batch(2); !slices.Equal(got, []int64{5, 1}) {
		t.Fatalf("wrapping batch: %v", got)
	}
	// Fewer members than a batch: each once, not twice.
	if got := r.batch(100); !slices.Equal(got, []int64{2, 3, 4, 5, 1}) {
		t.Fatalf("whole round: %v", got)
	}
	// A shorter list doesn't leave the place past its end.
	r.set([]int64{7, 8})
	r.batch(1)
	r.set([]int64{9})
	if got := r.batch(1); !slices.Equal(got, []int64{9}) {
		t.Fatalf("after shrinking: %v", got)
	}
}
