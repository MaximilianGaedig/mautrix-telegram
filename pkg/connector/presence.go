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

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/presence"
)

// mapTelegramStatus turns a Telegram user status into presence. Only what Telegram actually says is
// used: online (until it expires) and offline. The vague statuses of users who hide their last seen
// ("recently", "within a week/month") say nothing about now and are ignored; for those users the
// bridge sees activity instead (messages, typing, read receipts; see noteActivity).
func mapTelegramStatus(status tg.UserStatusClass, now time.Time) (presence.State, bool) {
	switch s := status.(type) {
	case *tg.UserStatusOnline:
		until := time.Unix(int64(s.Expires), 0)
		if s.Expires > 0 && !until.After(now) {
			return presence.State{Presence: event.PresenceOffline}, true
		}
		if s.Expires <= 0 {
			until = time.Time{}
		}
		return presence.State{Presence: event.PresenceOnline, Until: until}, true
	case *tg.UserStatusOffline:
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
	tc.main.presence.Update(string(ids.MakeUserID(userID)), st)
}

// noteActivity marks a sender online for a while after they did something (see presence.Manager.Activity).
func (tc *TelegramClient) noteActivity(sender bridgev2.EventSender, at time.Time) {
	if tc.main.presence == nil || sender.IsFromMe || sender.Sender == "" || sender.Sender == tc.userID {
		return
	}
	tc.main.presence.Activity(string(sender.Sender), at)
}
