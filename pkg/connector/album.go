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
	"strconv"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// AlbumFieldKey is the top-level content field that marks Matrix media events
// which were sent together as one album on Telegram.
//
// Every album item is still bridged as its own m.room.message event (rather
// than a single com.beeper.gallery event) so that clients which don't know
// about albums keep rendering each item normally. Clients that do can group
// events with the same fi.mau.album.id.
const AlbumFieldKey = "fi.mau.album"

// maxAlbumSize is the maximum number of items in a Telegram album.
const maxAlbumSize = 10

// AlbumInfo is the value of the fi.mau.album field.
type AlbumInfo struct {
	// ID is a stable opaque identifier, unique within the portal.
	ID string `json:"id"`
	// Index is the 0-based position of the item within the album.
	Index int `json:"index"`
	// Count is the total number of items, omitted when unknown. Telegram
	// delivers album items as separate updates, so it is always omitted here.
	Count int `json:"count,omitempty"`
}

func telegramAlbumID(groupedID int64) string {
	return "tg:" + strconv.FormatInt(groupedID, 10)
}

// albumGroupLookup returns the grouped ID of the given Telegram message ID in
// the current portal, or ok=false if the message is unknown or not grouped.
type albumGroupLookup func(ctx context.Context, msgID int) (groupedID int64, ok bool)

// telegramAlbumIndex computes the index of msgID inside the album groupedID.
//
// The messages of a Telegram album are created atomically by
// messages.sendMultiMedia and therefore have consecutive IDs, and an album has
// at most 10 items. The index is the distance to the smallest known message ID
// of the same group within the 9 preceding IDs. All 9 IDs are checked (rather
// than stopping at the first gap), so deleted items in the middle of an album
// don't reset the index of the later ones.
func telegramAlbumIndex(ctx context.Context, msgID int, groupedID int64, lookup albumGroupLookup) int {
	base := msgID
	for delta := 1; delta < maxAlbumSize; delta++ {
		otherID := msgID - delta
		if otherID <= 0 {
			break
		}
		if gid, ok := lookup(ctx, otherID); ok && gid == groupedID {
			base = otherID
		}
	}
	return msgID - base
}

type albumBatchContextKey struct{}

// withAlbumBatch stores the grouped IDs of a batch of messages which are being
// converted together (i.e. backfill) in the context. Those messages aren't in
// the database yet, so the database lookup alone couldn't find them.
func withAlbumBatch(ctx context.Context, messages []tg.MessageClass) context.Context {
	batch := make(map[int]int64)
	for _, msg := range messages {
		if m, ok := msg.(*tg.Message); ok && m.GroupedID != 0 {
			batch[m.ID] = m.GroupedID
		}
	}
	if len(batch) == 0 {
		return ctx
	}
	return context.WithValue(ctx, albumBatchContextKey{}, batch)
}

func (tc *TelegramClient) albumLookup(portal *bridgev2.Portal) albumGroupLookup {
	return func(ctx context.Context, msgID int) (int64, bool) {
		if batch, ok := ctx.Value(albumBatchContextKey{}).(map[int]int64); ok {
			if gid, ok := batch[msgID]; ok {
				return gid, true
			}
		}
		dbMsg, err := tc.main.Bridge.DB.Message.GetFirstPartByID(ctx, portal.Receiver, ids.MakeMessageID(portal.PortalKey, msgID))
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Int("message_id", msgID).Msg("Failed to look up message for album index")
			return 0, false
		} else if dbMsg == nil {
			return 0, false
		}
		meta, ok := dbMsg.Metadata.(*MessageMetadata)
		if !ok || meta.GroupedID == 0 {
			return 0, false
		}
		return meta.GroupedID, true
	}
}

func isAlbumableMsgType(msgType event.MessageType) bool {
	switch msgType {
	case event.MsgImage, event.MsgVideo, event.MsgFile, event.MsgAudio:
		return true
	default:
		return false
	}
}

// tagAlbumParts adds the album field to every media part of the message.
func tagAlbumParts(parts []*bridgev2.ConvertedMessagePart, info *AlbumInfo) {
	if info == nil {
		return
	}
	for _, part := range parts {
		if part.Type != event.EventMessage || part.Content == nil || !isAlbumableMsgType(part.Content.MsgType) {
			continue
		}
		if part.Extra == nil {
			part.Extra = make(map[string]any)
		}
		part.Extra[AlbumFieldKey] = info
	}
}

func (tc *TelegramClient) getAlbumInfo(ctx context.Context, portal *bridgev2.Portal, msg *tg.Message) *AlbumInfo {
	if msg.GroupedID == 0 {
		return nil
	}
	return &AlbumInfo{
		ID:    telegramAlbumID(msg.GroupedID),
		Index: telegramAlbumIndex(ctx, msg.ID, msg.GroupedID, tc.albumLookup(portal)),
	}
}

// trimTrailingAlbum removes the oldest album from a backwards backfill batch
// (sorted newest first) if it may be cut off by the batch boundary. The trimmed
// items are fetched again as a whole in the next batch, because the next
// request is anchored on the oldest message that was actually bridged. Without
// this, the newer half of the album would be bridged before the older half is
// known, and would get wrong indexes.
func trimTrailingAlbum(messages []tg.MessageClass) []tg.MessageClass {
	if len(messages) == 0 {
		return messages
	}
	last, ok := messages[len(messages)-1].(*tg.Message)
	if !ok || last.GroupedID == 0 {
		return messages
	}
	cut := len(messages) - 1
	for cut > 0 {
		m, ok := messages[cut-1].(*tg.Message)
		if !ok || m.GroupedID != last.GroupedID {
			break
		}
		cut--
	}
	if cut == 0 {
		// The whole batch is one album, keep it rather than making no progress.
		return messages
	}
	return messages[:cut]
}
