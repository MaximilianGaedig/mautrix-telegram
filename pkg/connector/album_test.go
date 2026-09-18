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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func mapLookup(m map[int]int64) albumGroupLookup {
	return func(_ context.Context, msgID int) (int64, bool) {
		gid, ok := m[msgID]
		return gid, ok
	}
}

func TestTelegramAlbumIndex(t *testing.T) {
	ctx := context.Background()
	known := map[int]int64{100: 7, 101: 7, 102: 7, 99: 8, 90: 7}
	// First item: nothing earlier in the same group.
	assert.Equal(t, 0, telegramAlbumIndex(ctx, 100, 7, mapLookup(known)))
	assert.Equal(t, 1, telegramAlbumIndex(ctx, 101, 7, mapLookup(known)))
	assert.Equal(t, 3, telegramAlbumIndex(ctx, 103, 7, mapLookup(known)))
	// A message of another group right before doesn't count.
	assert.Equal(t, 0, telegramAlbumIndex(ctx, 100, 7, mapLookup(map[int]int64{99: 8})))
	// A deleted item in the middle doesn't reset the index.
	assert.Equal(t, 2, telegramAlbumIndex(ctx, 102, 7, mapLookup(map[int]int64{100: 7})))
	// Messages more than 9 IDs back can't be part of the same album.
	assert.Equal(t, 9, telegramAlbumIndex(ctx, 99, 7, mapLookup(known)))
	assert.Equal(t, 0, telegramAlbumIndex(ctx, 100, 7, mapLookup(map[int]int64{90: 7})))
	// Low message IDs don't look up non-positive IDs.
	assert.Equal(t, 1, telegramAlbumIndex(ctx, 2, 5, func(_ context.Context, id int) (int64, bool) {
		require.Greater(t, id, 0)
		return 5, true
	}))
}

func TestAlbumBatchContext(t *testing.T) {
	ctx := withAlbumBatch(context.Background(), []tg.MessageClass{
		&tg.Message{ID: 12, GroupedID: 42},
		&tg.Message{ID: 11, GroupedID: 42},
		&tg.MessageService{ID: 10},
		&tg.Message{ID: 9},
	})
	batch := ctx.Value(albumBatchContextKey{}).(map[int]int64)
	assert.Equal(t, map[int]int64{12: 42, 11: 42}, batch)
	assert.Nil(t, withAlbumBatch(context.Background(), []tg.MessageClass{&tg.Message{ID: 1}}).Value(albumBatchContextKey{}))
}

func TestTrimTrailingAlbum(t *testing.T) {
	msgs := []tg.MessageClass{
		&tg.Message{ID: 20},
		&tg.Message{ID: 19, GroupedID: 1},
		&tg.Message{ID: 18, GroupedID: 1},
	}
	assert.Len(t, trimTrailingAlbum(msgs), 1)
	// Oldest message not in an album: untouched.
	assert.Len(t, trimTrailingAlbum(msgs[:1]), 1)
	// Whole batch is a single album: keep it to make progress.
	assert.Len(t, trimTrailingAlbum(msgs[1:]), 2)
	// A different album before the trailing one is kept.
	msgs2 := []tg.MessageClass{
		&tg.Message{ID: 20, GroupedID: 2},
		&tg.Message{ID: 19, GroupedID: 1},
	}
	assert.Len(t, trimTrailingAlbum(msgs2), 1)
	assert.Empty(t, trimTrailingAlbum(nil))
}

func TestTagAlbumParts(t *testing.T) {
	image := &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{MsgType: event.MsgImage}}
	text := &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{MsgType: event.MsgText}}
	sticker := &bridgev2.ConvertedMessagePart{Type: event.EventSticker, Content: &event.MessageEventContent{}}
	tagAlbumParts([]*bridgev2.ConvertedMessagePart{image, text, sticker}, &AlbumInfo{ID: telegramAlbumID(123), Index: 2})
	require.Contains(t, image.Extra, AlbumFieldKey)
	assert.NotContains(t, text.Extra, AlbumFieldKey)
	assert.NotContains(t, sticker.Extra, AlbumFieldKey)
	data, err := json.Marshal(image.Extra[AlbumFieldKey])
	require.NoError(t, err)
	// Telegram never knows the count, so it must be omitted.
	assert.JSONEq(t, `{"id":"tg:123","index":2}`, string(data))

	// No album: nothing is added.
	single := &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{MsgType: event.MsgImage}}
	tagAlbumParts([]*bridgev2.ConvertedMessagePart{single}, nil)
	assert.Nil(t, single.Extra)
}

func TestGetAlbumInfoSingle(t *testing.T) {
	var tc TelegramClient
	assert.Nil(t, tc.getAlbumInfo(context.Background(), nil, &tg.Message{ID: 5}))
}

func TestAlbumEditPartKeepsField(t *testing.T) {
	part := &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{MsgType: event.MsgVideo}}
	tagAlbumParts([]*bridgev2.ConvertedMessagePart{part}, &AlbumInfo{ID: "tg:1", Index: 1})
	edit := part.ToEditPart(nil)
	// Extra of an edit part ends up in m.new_content.
	assert.Contains(t, edit.Extra, AlbumFieldKey)
}
