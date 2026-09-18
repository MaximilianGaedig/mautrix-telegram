package connector

import (
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
