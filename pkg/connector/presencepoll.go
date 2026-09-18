// mautrix-telegram - A Matrix-Telegram puppeting bridge.
// Copyright (C) 2026 Maximilian Gaedig
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

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

const (
	presencePollInterval = time.Minute
	// DM partners that aren't contacts are fetched with users.getUsers, which
	// accepts at most this many IDs per call.
	presencePollUsersBatch = 100
	// Re-read the list of DM portals from the database every this many polls.
	presencePollDMRefresh = 10
)

// pollPresence periodically asks Telegram for statuses instead of relying only
// on updateUserStatus pushes. Telegram mostly pushes status updates to sessions
// that are marked online, and the bridge never marks itself online (that would
// show the user as online to everyone), so without polling most statuses never
// arrive.
func (tc *TelegramClient) pollPresence(ctx context.Context) {
	if tc.main.presence == nil || tc.metadata.IsBot {
		return
	}
	log := zerolog.Ctx(ctx).With().Str("action", "poll presence").Logger()
	ctx = log.WithContext(ctx)
	ticker := time.NewTicker(presencePollInterval)
	defer ticker.Stop()
	var dmUsers []int64
	for i := 0; ; i++ {
		contacts := tc.pollContactStatuses(ctx)
		if i%presencePollDMRefresh == 0 {
			dmUsers = tc.dmPartnerIDs(ctx)
		}
		others := tc.pollUserStatuses(ctx, dmUsers, contacts)
		log.Debug().Int("contacts", len(contacts)).Int("dm_non_contacts", others).Msg("Polled Telegram statuses")
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pollContactStatuses fetches all contacts' statuses in one call and returns
// the set of contact user IDs.
func (tc *TelegramClient) pollContactStatuses(ctx context.Context) map[int64]struct{} {
	statuses, err := tc.client.API().ContactsGetStatuses(ctx)
	if err != nil {
		if ctx.Err() == nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to get contact statuses")
		}
		return nil
	}
	seen := make(map[int64]struct{}, len(statuses))
	for _, st := range statuses {
		seen[st.UserID] = struct{}{}
		tc.handleUserStatus(st.UserID, st.Status)
	}
	return seen
}

// dmPartnerIDs lists the Telegram users this login has DM portals with.
func (tc *TelegramClient) dmPartnerIDs(ctx context.Context) []int64 {
	portals, err := tc.main.Bridge.DB.Portal.GetAllWithMXID(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to list portals for presence polling")
		return nil
	}
	var out []int64
	for _, p := range portals {
		if p.Receiver != tc.userLogin.ID {
			continue
		}
		peerType, id, _, err := ids.ParsePortalID(p.ID)
		if err != nil || peerType != ids.PeerTypeUser || id == tc.telegramUserID {
			continue
		}
		out = append(out, id)
	}
	return out
}

// pollUserStatuses fetches statuses of DM partners that aren't contacts and
// returns how many were requested.
func (tc *TelegramClient) pollUserStatuses(ctx context.Context, userIDs []int64, contacts map[int64]struct{}) int {
	var inputs []tg.InputUserClass
	for _, id := range userIDs {
		if _, isContact := contacts[id]; isContact {
			continue
		}
		accessHash, err := tc.ScopedStore.GetAccessHash(ctx, ids.PeerTypeUser, id)
		if err != nil || accessHash == 0 {
			continue
		}
		inputs = append(inputs, &tg.InputUser{UserID: id, AccessHash: accessHash})
	}
	for start := 0; start < len(inputs); start += presencePollUsersBatch {
		end := min(start+presencePollUsersBatch, len(inputs))
		users, err := tc.client.API().UsersGetUsers(ctx, inputs[start:end])
		if err != nil {
			if ctx.Err() == nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to get DM partner statuses")
			}
			return len(inputs)
		}
		for _, u := range users {
			if user, ok := u.(*tg.User); ok {
				if status, ok := user.GetStatus(); ok {
					tc.handleUserStatus(user.ID, status)
				}
			}
		}
	}
	return len(inputs)
}
