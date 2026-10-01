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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// These cases all have the full reaction list in the update itself, so computing the list needs
// neither a Telegram connection nor the database and an empty client is enough.

func TestComputeReactionsListSkipsPaidReaction(t *testing.T) {
	peer := &tg.PeerChannel{ChannelID: 100}
	thumbsUp := &tg.ReactionEmoji{Emoticon: "👍"}
	heart := &tg.ReactionEmoji{Emoticon: "❤"}
	reactions := tg.MessageReactions{
		CanSeeList: true,
		Results: []tg.ReactionCount{
			{Reaction: thumbsUp, Count: 1},
			{Reaction: &tg.ReactionPaid{}, Count: 1},
			{Reaction: heart, Count: 1},
		},
		RecentReactions: []tg.MessagePeerReaction{
			{PeerID: &tg.PeerUser{UserID: 1}, Reaction: thumbsUp},
			{PeerID: &tg.PeerUser{UserID: 2}, Reaction: &tg.ReactionPaid{}},
			{PeerID: &tg.PeerUser{UserID: 3}, Reaction: heart},
		},
	}

	list, isFull, _, err := (&TelegramClient{}).computeReactionsList(context.Background(), peer, 1, reactions)
	require.NoError(t, err, "a paid reaction must not fail the sync of the other reactions")
	require.Len(t, list, 2)
	assert.Equal(t, thumbsUp, list[0].Reaction)
	assert.Equal(t, heart, list[1].Reaction)
	assert.True(t, isFull, "the list has every bridgeable reaction, so stale ones may be removed")
}

func TestComputeReactionsListIgnoresStarCount(t *testing.T) {
	// The count of a paid reaction is the number of stars, not of people. If it were counted,
	// the list would look incomplete and the bridge would try to fetch the rest from Telegram.
	thumbsUp := &tg.ReactionEmoji{Emoticon: "👍"}
	reactions := tg.MessageReactions{
		CanSeeList: true,
		Results: []tg.ReactionCount{
			{Reaction: thumbsUp, Count: 1},
			{Reaction: &tg.ReactionPaid{}, Count: 250},
		},
		RecentReactions: []tg.MessagePeerReaction{
			{PeerID: &tg.PeerUser{UserID: 1}, Reaction: thumbsUp},
		},
	}

	list, isFull, _, err := (&TelegramClient{}).computeReactionsList(context.Background(), &tg.PeerChannel{ChannelID: 100}, 1, reactions)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, isFull)
}

func TestComputeReactionsListOnlyPaidReactions(t *testing.T) {
	reactions := tg.MessageReactions{
		Results: []tg.ReactionCount{{Reaction: &tg.ReactionPaid{}, Count: 10}},
		RecentReactions: []tg.MessagePeerReaction{
			{PeerID: &tg.PeerUser{UserID: 2}, Reaction: &tg.ReactionPaid{}},
		},
	}

	list, isFull, _, err := (&TelegramClient{}).computeReactionsList(context.Background(), &tg.PeerChannel{ChannelID: 100}, 1, reactions)
	require.NoError(t, err)
	assert.Empty(t, list)
	assert.True(t, isFull)
}

func TestIsBridgeableReaction(t *testing.T) {
	assert.True(t, isBridgeableReaction(&tg.ReactionEmoji{Emoticon: "👍"}))
	assert.True(t, isBridgeableReaction(&tg.ReactionCustomEmoji{DocumentID: 1}))
	assert.False(t, isBridgeableReaction(&tg.ReactionPaid{}))
	assert.False(t, isBridgeableReaction(&tg.ReactionEmpty{}))
	assert.False(t, isBridgeableReaction(nil))
}
