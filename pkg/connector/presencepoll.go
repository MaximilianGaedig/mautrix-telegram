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
	"slices"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tgerr"
)

const (
	presencePollInterval = 15 * time.Second
	// DM partners that aren't contacts are fetched with users.getUsers, which
	// accepts at most this many IDs per call.
	presencePollUsersBatch = 100
	// Re-read the list of DM portals from the database every this many polls.
	presencePollDMRefresh = 10
	// Re-read who is in the groups every this many polls (ten minutes).
	presencePollMembersRefresh = 40
)

// memberRotation hands out group members a batch at a time, round and round: there are too many to
// ask about at once, and nobody needs a stranger's status to the second.
type memberRotation struct {
	ids  []int64
	next int
}

// set replaces the members, keeping the place in the round.
func (r *memberRotation) set(ids []int64) {
	r.ids = ids
	if r.next >= len(ids) {
		r.next = 0
	}
}

// batch returns the next up to n members, wrapping around once at most.
func (r *memberRotation) batch(n int) []int64 {
	n = min(n, len(r.ids))
	out := make([]int64, 0, n)
	for range n {
		out = append(out, r.ids[r.next])
		r.next = (r.next + 1) % len(r.ids)
	}
	return out
}

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
	var dmUsers []int64
	var members memberRotation
	for i := 0; ; i++ {
		contacts, wait := tc.pollContactStatuses(ctx)
		if i%presencePollDMRefresh == 0 {
			dmUsers = tc.dmPartnerIDs(ctx)
		}
		others, otherWait := tc.pollUserStatuses(ctx, dmUsers, contacts)
		wait = max(wait, otherWait)
		groupMembers := 0
		if tc.main.Config.PresenceGroupMembers {
			if i%presencePollMembersRefresh == 0 {
				members.set(tc.groupMemberIDs(ctx, contacts, dmUsers))
			}
			// One batch a poll: the whole round takes a few minutes, at one extra request each time.
			if wait == 0 {
				var memberWait time.Duration
				groupMembers, memberWait = tc.pollUserStatuses(ctx, members.batch(presencePollUsersBatch), contacts)
				wait = max(wait, memberWait)
			}
		}
		log.Debug().
			Int("contacts", len(contacts)).
			Int("dm_non_contacts", others).
			Int("group_members", groupMembers).
			Int("group_members_total", len(members.ids)).
			Msg("Polled Telegram statuses")
		// Telegram answers a poll that comes too often with a flood wait. Asking again before it has
		// passed only earns another one and spends request budget the history import needs, so the next
		// poll waits it out.
		next := max(presencePollInterval, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		}
	}
}

// pollContactStatuses fetches all contacts' statuses in one call and returns
// the set of contact user IDs.
// It also reports how long Telegram asked the bridge to wait before polling again, if it did.
func (tc *TelegramClient) pollContactStatuses(ctx context.Context) (map[int64]struct{}, time.Duration) {
	statuses, err := tc.client.API().ContactsGetStatuses(ctx)
	if err != nil {
		wait, isFlood := tgerr.AsFloodWait(err)
		if ctx.Err() == nil && !isFlood {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to get contact statuses")
		} else if isFlood {
			zerolog.Ctx(ctx).Debug().Dur("wait", wait).Msg("Telegram asked to poll statuses less often")
		}
		return nil, wait
	}
	seen := make(map[int64]struct{}, len(statuses))
	for _, st := range statuses {
		seen[st.UserID] = struct{}{}
		tc.handleUserStatus(st.UserID, st.Status)
	}
	return seen, 0
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

// groupMemberIDs lists everyone in this login's group chats, as the rooms' members show them, but
// for those whose status is polled already: contacts and DM partners.
func (tc *TelegramClient) groupMemberIDs(ctx context.Context, contacts map[int64]struct{}, dmUsers []int64) []int64 {
	portals, err := tc.main.Bridge.DB.Portal.GetAllWithMXID(ctx)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to list portals for group member presence")
		return nil
	}
	skip := make(map[int64]struct{}, len(contacts)+len(dmUsers)+1)
	for id := range contacts {
		skip[id] = struct{}{}
	}
	for _, id := range dmUsers {
		skip[id] = struct{}{}
	}
	skip[tc.telegramUserID] = struct{}{}
	var out []int64
	for _, p := range portals {
		if p.Receiver != "" && p.Receiver != tc.userLogin.ID {
			continue
		}
		peerType, _, _, err := ids.ParsePortalID(p.ID)
		if err != nil || peerType == ids.PeerTypeUser {
			continue
		}
		joined, err := tc.main.Bridge.Matrix.GetMembers(ctx, p.MXID)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Stringer("room_id", p.MXID).Msg("Failed to get members for presence")
			continue
		}
		for userID, member := range joined {
			if member == nil || member.Membership != event.MembershipJoin {
				continue
			}
			ghost, ok := tc.main.Bridge.Matrix.ParseGhostMXID(userID)
			if !ok {
				continue
			}
			peerType, id, err := ids.ParseUserID(ghost)
			if err != nil || peerType != ids.PeerTypeUser {
				continue
			}
			if _, seen := skip[id]; !seen {
				skip[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	return out
}

// pollUserStatuses fetches statuses of users that aren't contacts and returns how many were
// requested, and how long Telegram asked to wait before asking again, if it did.
func (tc *TelegramClient) pollUserStatuses(ctx context.Context, userIDs []int64, contacts map[int64]struct{}) (int, time.Duration) {
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
			wait, isFlood := tgerr.AsFloodWait(err)
			if ctx.Err() == nil && !isFlood {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to get user statuses")
			}
			return len(inputs), wait
		}
		for _, u := range users {
			if user, ok := u.(*tg.User); ok {
				if status, ok := user.GetStatus(); ok {
					tc.handleUserStatus(user.ID, status)
				}
			}
		}
	}
	return len(inputs), 0
}
