// mautrix-telegram - A Matrix-Telegram puppeting bridge.
// Copyright (C) 2026 Sumner Evans
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/presence"
)

// LastSeenPrefix starts every status_msg the bridge sets, so clients can
// recognise and reformat it (e.g. "last seen 5 minutes ago").
const LastSeenPrefix = "last seen "

// mapTelegramStatus converts a Telegram user status to Matrix presence.
//
// Matrix's PUT /presence can't carry last_active_ago, so the exact last-seen
// time (UserStatusOffline.WasOnline) goes into status_msg as
// "last seen <RFC3339 UTC>". Hidden last-seen becomes "last seen recently",
// "last seen within a week" or "last seen within a month", like Telegram shows.
func mapTelegramStatus(status tg.UserStatusClass, now time.Time) (presence.State, bool) {
	switch s := status.(type) {
	case *tg.UserStatusOnline:
		until := time.Unix(int64(s.Expires), 0)
		if s.Expires > 0 && !until.After(now) {
			return presence.State{Presence: event.PresenceUnavailable}, true
		}
		if s.Expires <= 0 {
			until = time.Time{}
		}
		return presence.State{Presence: event.PresenceOnline, Until: until}, true
	case *tg.UserStatusOffline:
		st := presence.State{Presence: event.PresenceOffline}
		if s.WasOnline > 0 {
			st.StatusMsg = LastSeenPrefix + time.Unix(int64(s.WasOnline), 0).UTC().Format(time.RFC3339)
		}
		return st, true
	// Last seen is hidden by the user's privacy settings.
	case *tg.UserStatusRecently:
		return presence.State{Presence: event.PresenceUnavailable, StatusMsg: LastSeenPrefix + "recently"}, true
	case *tg.UserStatusLastWeek:
		return presence.State{Presence: event.PresenceUnavailable, StatusMsg: LastSeenPrefix + "within a week"}, true
	case *tg.UserStatusLastMonth:
		return presence.State{Presence: event.PresenceUnavailable, StatusMsg: LastSeenPrefix + "within a month"}, true
	case *tg.UserStatusEmpty:
		return presence.State{Presence: event.PresenceOffline}, true
	default:
		return presence.State{}, false
	}
}

func (tc *TelegramConnector) startPresence(ctx context.Context) {
	if !tc.Config.PresenceBridging {
		return
	}
	tc.presence = presence.NewManager(presence.Config{
		Refresh:       time.Duration(tc.Config.PresenceRefreshSeconds) * time.Second,
		RatePerSecond: tc.Config.PresenceMaxPerSecond,
		// Telegram status changes are already coarse; forward them quickly.
		Debounce: 2 * time.Second,
	}, presence.GhostSender(tc.Bridge))
	log := tc.Bridge.Log.With().Str("component", "presence").Logger()
	bg := log.WithContext(context.WithoutCancel(ctx))
	tc.lastOnline.persist = func(userID int64, at time.Time) {
		ghost, err := tc.Bridge.GetGhostByID(bg, ids.MakeUserID(userID))
		if err != nil || ghost == nil {
			return
		}
		meta, ok := ghost.Metadata.(*GhostMetadata)
		if !ok || meta.LastOnline >= at.Unix() {
			return
		}
		meta.LastOnline = at.Unix()
		if err = tc.Bridge.DB.Ghost.Update(bg, ghost.Ghost); err != nil {
			log.Debug().Err(err).Int64("user_id", userID).Msg("Failed to save last online time")
		}
	}
	tc.lastOnline.load = func(userID int64) time.Time {
		ghost, err := tc.Bridge.GetGhostByID(bg, ids.MakeUserID(userID))
		if err != nil || ghost == nil {
			return time.Time{}
		}
		if meta, ok := ghost.Metadata.(*GhostMetadata); ok && meta.LastOnline > 0 {
			return time.Unix(meta.LastOnline, 0)
		}
		return time.Time{}
	}
	go tc.presence.Run(bg)
}

func (tc *TelegramClient) handleUserStatus(userID int64, status tg.UserStatusClass) {
	if tc.main.presence == nil || userID == 0 || userID == tc.telegramUserID || status == nil {
		return
	}
	now := time.Now()
	st, ok := mapTelegramStatus(status, now)
	if !ok {
		return
	}
	st = tc.main.lastOnline.apply(userID, st, now)
	tc.main.presence.Update(string(ids.MakeUserID(userID)), st)
}

// lastOnlineTracker remembers when Telegram last said each user was online. When Telegram later
// only reports a vague status ("recently" etc., because the exact last-seen time is hidden from us),
// that time is used instead, so clients can still show "last seen 21:40". It's accurate to roughly
// the status poll interval, and saved in the ghost's metadata (at most once a minute per user) so it
// survives restarts.
type lastOnlineTracker struct {
	lock      sync.Mutex
	seen      map[int64]time.Time
	persisted map[int64]time.Time
	loaded    map[int64]bool

	// persist saves a user's last online time; load reads it back. Both may be nil (tests).
	persist func(userID int64, at time.Time)
	load    func(userID int64) time.Time
}

// lastOnlinePersistEvery limits how often one user's last online time is written.
const lastOnlinePersistEvery = time.Minute

func (t *lastOnlineTracker) apply(userID int64, st presence.State, now time.Time) presence.State {
	vague := st.Presence == event.PresenceUnavailable && strings.HasPrefix(st.StatusMsg, LastSeenPrefix) &&
		!isExactLastSeen(st.StatusMsg)
	t.lock.Lock()
	if t.seen == nil {
		t.seen, t.persisted, t.loaded = map[int64]time.Time{}, map[int64]time.Time{}, map[int64]bool{}
	}
	_, known := t.seen[userID]
	needLoad := vague && !known && !t.loaded[userID] && t.load != nil
	t.lock.Unlock()
	var loaded time.Time
	if needLoad {
		loaded = t.load(userID)
	}

	t.lock.Lock()
	defer t.lock.Unlock()
	if needLoad {
		t.loaded[userID] = true
		if !loaded.IsZero() && loaded.After(t.seen[userID]) {
			t.seen[userID] = loaded
			t.persisted[userID] = loaded
		}
	}
	switch {
	case st.Presence == event.PresenceOnline:
		t.seen[userID] = now
		if t.persist != nil && now.Sub(t.persisted[userID]) >= lastOnlinePersistEvery {
			t.persisted[userID] = now
			go t.persist(userID, now)
		}
	case vague:
		if last, ok := t.seen[userID]; ok {
			st.StatusMsg = LastSeenPrefix + last.UTC().Truncate(time.Second).Format(time.RFC3339)
		}
	}
	return st
}

func isExactLastSeen(msg string) bool {
	_, err := time.Parse(time.RFC3339, strings.TrimPrefix(msg, LastSeenPrefix))
	return err == nil
}
