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
	"time"

	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/presence"
)

// mapTelegramStatus converts a Telegram user status to Matrix presence.
//
// Matrix's PUT /presence can't carry last_active_ago, so exact last-seen
// timestamps (UserStatusOffline.WasOnline) are not bridged; the homeserver
// derives last_active_ago from when the ghost was last set online instead.
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
		return presence.State{Presence: event.PresenceOffline}, true
	case *tg.UserStatusRecently, *tg.UserStatusLastWeek, *tg.UserStatusLastMonth:
		// Last seen is hidden by the user's privacy settings.
		return presence.State{Presence: event.PresenceUnavailable}, true
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
	}, presence.GhostSender(tc.Bridge))
	log := tc.Bridge.Log.With().Str("component", "presence").Logger()
	go tc.presence.Run(log.WithContext(context.WithoutCancel(ctx)))
}

func (tc *TelegramClient) handleUserStatus(userID int64, status tg.UserStatusClass) {
	if tc.main.presence == nil || userID == 0 || userID == tc.telegramUserID || status == nil {
		return
	}
	if st, ok := mapTelegramStatus(status, time.Now()); ok {
		tc.main.presence.Update(string(ids.MakeUserID(userID)), st)
	}
}
