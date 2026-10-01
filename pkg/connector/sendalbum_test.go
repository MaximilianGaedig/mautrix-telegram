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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestAlbumKindOf(t *testing.T) {
	upload := &tg.InputFile{ID: 1, Parts: 1, Name: "upload"}
	tests := []struct {
		name          string
		content       event.MessageEventContent
		info          event.FileInfo
		forceDocument bool
		want          albumKind
	}{
		{name: "photo", content: event.MessageEventContent{MsgType: event.MsgImage}, info: event.FileInfo{MimeType: "image/jpeg"}, want: albumKindVisual},
		{name: "video", content: event.MessageEventContent{MsgType: event.MsgVideo}, info: event.FileInfo{MimeType: "video/mp4"}, want: albumKindVisual},
		{name: "file", content: event.MessageEventContent{MsgType: event.MsgFile}, info: event.FileInfo{MimeType: "application/pdf"}, want: albumKindDocument},
		{name: "image sent as a file", content: event.MessageEventContent{MsgType: event.MsgImage}, info: event.FileInfo{MimeType: "image/jpeg"}, forceDocument: true, want: albumKindDocument},
		{name: "image Telegram has no photo for", content: event.MessageEventContent{MsgType: event.MsgImage}, info: event.FileInfo{MimeType: "image/bmp"}, want: albumKindDocument},
		{name: "music", content: event.MessageEventContent{MsgType: event.MsgAudio}, info: event.FileInfo{MimeType: "audio/mpeg"}, want: albumKindAudio},
		{name: "voice message", content: event.MessageEventContent{MsgType: event.MsgAudio, MSC3245Voice: &event.MSC3245Voice{}}, info: event.FileInfo{MimeType: "audio/ogg"}, want: albumKindNone},
		{name: "video gif", content: event.MessageEventContent{MsgType: event.MsgVideo}, info: event.FileInfo{MimeType: "video/mp4", MauGIF: true}, want: albumKindNone},
		{name: "image gif", content: event.MessageEventContent{MsgType: event.MsgImage}, info: event.FileInfo{MimeType: "image/gif"}, want: albumKindNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			media := makeUploadedMedia(upload, &tt.content, &tt.info, "file", false, tt.forceDocument, false)
			assert.Equal(t, tt.want, albumKindOf(media))
		})
	}
	sticker := makeUploadedMedia(upload, &event.MessageEventContent{MsgType: event.MsgImage}, &event.FileInfo{MimeType: "image/webp"}, "sticker.webp", true, true, false)
	assert.Equal(t, albumKindNone, albumKindOf(sticker), "sticker")
	assert.Equal(t, albumKindNone, albumKindOf(&tg.InputMediaGeoPoint{}), "not a file at all")
}

func kindsOf(n int, kind albumKind) []albumKind {
	kinds := make([]albumKind, n)
	for i := range kinds {
		kinds[i] = kind
	}
	return kinds
}

func TestPlanAlbums(t *testing.T) {
	const v, d, a, none = albumKindVisual, albumKindDocument, albumKindAudio, albumKindNone
	t.Run("up to ten items are one album", func(t *testing.T) {
		assert.Equal(t, [][]int{{0, 1, 2}}, planAlbums(kindsOf(3, v)))
		assert.Equal(t, [][]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}}, planAlbums(kindsOf(10, v)))
	})
	t.Run("more than ten are split", func(t *testing.T) {
		assert.Equal(t, [][]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {10}}, planAlbums(kindsOf(11, v)))
		groups := planAlbums(kindsOf(23, d))
		require.Len(t, groups, 3)
		assert.Len(t, groups[0], 10)
		assert.Len(t, groups[1], 10)
		assert.Equal(t, []int{20, 21, 22}, groups[2])
	})
	t.Run("files don't mix with photos and videos", func(t *testing.T) {
		assert.Equal(t, [][]int{{0, 1}, {2, 3}, {4}}, planAlbums([]albumKind{v, v, d, d, v}))
	})
	t.Run("music doesn't mix with files", func(t *testing.T) {
		assert.Equal(t, [][]int{{0, 1}, {2, 3}}, planAlbums([]albumKind{a, a, d, d}))
	})
	t.Run("what can't be in an album goes alone and keeps its place", func(t *testing.T) {
		assert.Equal(t, [][]int{{0}, {1}, {2}, {3, 4}}, planAlbums([]albumKind{v, none, none, v, v}))
	})
	t.Run("nothing", func(t *testing.T) {
		assert.Empty(t, planAlbums(nil))
	})
}

func TestMakeMultiMedia(t *testing.T) {
	bold := []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 4}}
	multiMedia := makeMultiMedia([]*albumItem{
		{media: &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: 1}}, randomID: 11, message: "look at these", entities: bold},
		{media: &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: 2}}, randomID: 12},
		{media: &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 3}}, randomID: 13},
	})
	require.Len(t, multiMedia, 3)
	assert.Equal(t, "look at these", multiMedia[0].Message, "the caption is on the first item")
	assert.Equal(t, bold, multiMedia[0].Entities)
	for i, single := range multiMedia[1:] {
		assert.Empty(t, single.Message, "item %d has no caption", i+1)
		assert.Empty(t, single.Entities)
	}
	assert.Equal(t, []int64{11, 12, 13}, []int64{multiMedia[0].RandomID, multiMedia[1].RandomID, multiMedia[2].RandomID})
	assert.Equal(t, &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 3}}, multiMedia[2].Media)
}

func TestSavedAlbumMedia(t *testing.T) {
	photo, err := savedAlbumMedia(&tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 5, AccessHash: 6, FileReference: []byte{7}}}, true)
	require.NoError(t, err)
	assert.Equal(t, &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: 5, AccessHash: 6, FileReference: []byte{7}}, Spoiler: true}, photo)

	doc, err := savedAlbumMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: 8, AccessHash: 9, FileReference: []byte{10}}}, false)
	require.NoError(t, err)
	assert.Equal(t, &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 8, AccessHash: 9, FileReference: []byte{10}}}, doc)

	_, err = savedAlbumMedia(&tg.MessageMediaPhoto{Photo: &tg.PhotoEmpty{}}, false)
	assert.Error(t, err)
	_, err = savedAlbumMedia(&tg.MessageMediaEmpty{}, false)
	assert.Error(t, err)
}

func TestSplitAlbumUpdates(t *testing.T) {
	// Telegram doesn't promise any order, and sends other updates along.
	updates := &tg.Updates{
		Date: 1700000000,
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{Message: &tg.Message{ID: 502, GroupedID: 77}},
			&tg.UpdateMessageID{ID: 502, RandomID: 12},
			&tg.UpdateReadHistoryOutbox{},
			&tg.UpdateMessageID{ID: 501, RandomID: 11},
			&tg.UpdateNewMessage{Message: &tg.Message{ID: 501, GroupedID: 77}},
		},
	}
	split, err := splitAlbumUpdates(updates, []int64{11, 12, 13})
	require.NoError(t, err)
	require.Len(t, split, 3)
	assert.Equal(t, &tg.Updates{Date: 1700000000, Updates: []tg.UpdateClass{
		&tg.UpdateMessageID{ID: 501, RandomID: 11},
		&tg.UpdateNewMessage{Message: &tg.Message{ID: 501, GroupedID: 77}},
	}}, split[0])
	assert.Equal(t, 502, split[1].Updates[0].(*tg.UpdateMessageID).ID)
	assert.Nil(t, split[2], "an item Telegram didn't answer for")

	_, err = splitAlbumUpdates(&tg.UpdateShortSentMessage{ID: 1}, []int64{11})
	assert.Error(t, err, "an album is never answered with a short update")
}

// TestSplitAlbumUpdatesFeedsMakeSendResponse checks that the per-item updates are what the
// single message code expects, so that every Matrix event gets the ID of its own Telegram message.
func TestSplitAlbumUpdatesFeedsMakeSendResponse(t *testing.T) {
	portal := &bridgev2.Portal{Portal: &database.Portal{PortalKey: networkid.PortalKey{ID: ids.MakePortalID(ids.PeerTypeChannel, 100)}}}
	updates := &tg.Updates{
		Date: 1700000000,
		Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 501, RandomID: 11},
			&tg.UpdateMessageID{ID: 502, RandomID: 12},
			&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 501, GroupedID: 77, Date: 1700000001}},
			&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 502, GroupedID: 77, Date: 1700000001}},
		},
	}
	// The Matrix events are given in the opposite order of Telegram's answer.
	randomIDs := []int64{12, 11}
	split, err := splitAlbumUpdates(updates, randomIDs)
	require.NoError(t, err)
	tc := &TelegramClient{userID: "42"}
	var got []networkid.MessageID
	for i, randomID := range randomIDs {
		msg := &bridgev2.MatrixMessage{MatrixEventBase: bridgev2.MatrixEventBase[*event.MessageEventContent]{Portal: portal}}
		resp, err := tc.makeSendResponse(context.Background(), msg, split[i], randomID, "", "photo.jpg")
		require.NoError(t, err)
		got = append(got, resp.DB.ID)
		assert.Equal(t, networkid.UserID("42"), resp.DB.SenderID)
		assert.EqualValues(t, 77, albumGroupedID(split[i]))
	}
	assert.Equal(t, []networkid.MessageID{ids.MakeMessageID(portal.PortalKey, 502), ids.MakeMessageID(portal.PortalKey, 501)}, got)
	assert.NotEqual(t, got[0], got[1])
}

// fakeAlbumAPI stands in for Telegram. It gives every sent item the next message ID.
type fakeAlbumAPI struct {
	uploads    []tg.InputMediaClass
	singles    []*tg.MessagesSendMediaRequest
	albums     []*tg.MessagesSendMultiMediaRequest
	nextID     int
	failUpload map[int64]error
	failAlbum  map[int]error
}

func (f *fakeAlbumAPI) MessagesUploadMedia(ctx context.Context, req *tg.MessagesUploadMediaRequest) (tg.MessageMediaClass, error) {
	f.uploads = append(f.uploads, req.Media)
	switch media := req.Media.(type) {
	case *tg.InputMediaUploadedPhoto:
		fileID := media.File.(*tg.InputFile).ID
		if err := f.failUpload[fileID]; err != nil {
			return nil, err
		}
		return &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: fileID + 1000}}, nil
	case *tg.InputMediaUploadedDocument:
		fileID := media.File.(*tg.InputFile).ID
		if err := f.failUpload[fileID]; err != nil {
			return nil, err
		}
		return &tg.MessageMediaDocument{Document: &tg.Document{ID: fileID + 1000}}, nil
	default:
		return nil, errors.New("only freshly uploaded files need saving")
	}
}

func (f *fakeAlbumAPI) MessagesSendMedia(ctx context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	f.singles = append(f.singles, req)
	f.nextID++
	return &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateMessageID{ID: f.nextID, RandomID: req.RandomID},
		&tg.UpdateNewMessage{Message: &tg.Message{ID: f.nextID}},
	}}, nil
}

func (f *fakeAlbumAPI) MessagesSendMultiMedia(ctx context.Context, req *tg.MessagesSendMultiMediaRequest) (tg.UpdatesClass, error) {
	f.albums = append(f.albums, req)
	if err := f.failAlbum[len(f.albums)-1]; err != nil {
		return nil, err
	}
	updates := &tg.Updates{}
	for _, single := range req.MultiMedia {
		f.nextID++
		updates.Updates = append(updates.Updates,
			&tg.UpdateMessageID{ID: f.nextID, RandomID: single.RandomID},
			&tg.UpdateNewMessage{Message: &tg.Message{ID: f.nextID, GroupedID: int64(len(f.albums))}},
		)
	}
	return updates, nil
}

func fakePhotoItem(n int64) *albumItem {
	return &albumItem{media: &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: n}}, randomID: n}
}

func fakeFileItem(n int64) *albumItem {
	return &albumItem{
		media: &tg.InputMediaUploadedDocument{
			File:       &tg.InputFile{ID: n},
			MimeType:   "application/pdf",
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "a.pdf"}},
		},
		randomID: n,
	}
}

func sentMessageID(t *testing.T, item *albumItem) int {
	t.Helper()
	require.NoError(t, item.err)
	require.NotNil(t, item.updates)
	return item.updates.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
}

func TestSendAlbumItems_OneAlbum(t *testing.T) {
	api := &fakeAlbumAPI{}
	peer := &tg.InputPeerUser{UserID: 1}
	replyTo := &tg.InputReplyToMessage{ReplyToMsgID: 9}
	items := []*albumItem{fakePhotoItem(1), fakePhotoItem(2), fakePhotoItem(3)}
	items[0].message = "caption"
	items[1].media.(*tg.InputMediaUploadedPhoto).Spoiler = true

	sendAlbumItems(context.Background(), api, peer, replyTo, items)

	assert.Len(t, api.uploads, 3, "every file is saved on Telegram first")
	assert.Empty(t, api.singles)
	require.Len(t, api.albums, 1, "one request for the whole album")
	req := api.albums[0]
	assert.Equal(t, peer, req.Peer)
	assert.Equal(t, replyTo, req.ReplyTo)
	require.Len(t, req.MultiMedia, 3)
	assert.Equal(t, &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: 1001}}, req.MultiMedia[0].Media, "the album refers to the saved photo, not the upload")
	assert.True(t, req.MultiMedia[1].Media.(*tg.InputMediaPhoto).Spoiler, "a spoiler survives saving")
	assert.Equal(t, "caption", req.MultiMedia[0].Message)
	assert.Empty(t, req.MultiMedia[1].Message)
	for i, item := range items {
		assert.Equal(t, i+1, sentMessageID(t, item), "every item knows its own message")
		assert.EqualValues(t, 1, albumGroupedID(item.updates))
	}
}

func TestSendAlbumItems_SplitsAfterTen(t *testing.T) {
	api := &fakeAlbumAPI{}
	var items []*albumItem
	for n := int64(1); n <= 12; n++ {
		items = append(items, fakePhotoItem(n))
	}
	sendAlbumItems(context.Background(), api, &tg.InputPeerSelf{}, nil, items)
	require.Len(t, api.albums, 2)
	assert.Len(t, api.albums[0].MultiMedia, 10)
	assert.Len(t, api.albums[1].MultiMedia, 2)
	assert.EqualValues(t, 11, api.albums[1].MultiMedia[0].RandomID, "the second album continues where the first ended")
	for i, item := range items {
		assert.Equal(t, i+1, sentMessageID(t, item))
	}
}

func TestSendAlbumItems_FailedAlbumLeavesTheOthersSent(t *testing.T) {
	failure := errors.New("FLOOD_WAIT")
	api := &fakeAlbumAPI{failAlbum: map[int]error{1: failure}}
	var items []*albumItem
	for n := int64(1); n <= 12; n++ {
		items = append(items, fakePhotoItem(n))
	}
	sendAlbumItems(context.Background(), api, &tg.InputPeerSelf{}, nil, items)
	for i, item := range items[:10] {
		assert.Equal(t, i+1, sentMessageID(t, item))
	}
	for _, item := range items[10:] {
		assert.ErrorIs(t, item.err, failure)
		assert.Nil(t, item.updates)
	}
}

func TestSendAlbumItems_FilesGetTheirOwnAlbum(t *testing.T) {
	api := &fakeAlbumAPI{}
	items := []*albumItem{fakePhotoItem(1), fakePhotoItem(2), fakeFileItem(3), fakeFileItem(4), fakePhotoItem(5)}
	sendAlbumItems(context.Background(), api, &tg.InputPeerSelf{}, &tg.InputReplyToMessage{ReplyToMsgID: 9}, items)

	require.Len(t, api.albums, 2)
	assert.IsType(t, &tg.InputMediaPhoto{}, api.albums[0].MultiMedia[0].Media)
	assert.Len(t, api.albums[0].MultiMedia, 2)
	assert.IsType(t, &tg.InputMediaDocument{}, api.albums[1].MultiMedia[0].Media)
	assert.Len(t, api.albums[1].MultiMedia, 2)
	// The photo left over at the end is an ordinary message, sent the ordinary way.
	require.Len(t, api.singles, 1)
	assert.Same(t, items[4].media, api.singles[0].Media)
	assert.IsType(t, &tg.InputMediaUploadedPhoto{}, api.singles[0].Media)
	assert.EqualValues(t, 5, api.singles[0].RandomID)
	assert.Equal(t, &tg.InputReplyToMessage{ReplyToMsgID: 9}, api.singles[0].ReplyTo)
	assert.Len(t, api.uploads, 4, "a file sent on its own isn't saved first")
	for i, item := range items {
		assert.Equal(t, i+1, sentMessageID(t, item), "the order of the items is kept")
	}
}

func TestSendAlbumItems_FileThatCannotBeSavedLeavesTheAlbum(t *testing.T) {
	failure := errors.New("PHOTO_INVALID_DIMENSIONS")
	t.Run("the rest is still an album", func(t *testing.T) {
		api := &fakeAlbumAPI{failUpload: map[int64]error{2: failure}}
		items := []*albumItem{fakePhotoItem(1), fakePhotoItem(2), fakePhotoItem(3)}
		sendAlbumItems(context.Background(), api, &tg.InputPeerSelf{}, nil, items)
		require.Len(t, api.albums, 1)
		assert.Len(t, api.albums[0].MultiMedia, 2)
		assert.ErrorIs(t, items[1].err, failure)
		assert.Equal(t, 1, sentMessageID(t, items[0]))
		assert.Equal(t, 2, sentMessageID(t, items[2]))
	})
	t.Run("a single item left is an ordinary message", func(t *testing.T) {
		api := &fakeAlbumAPI{failUpload: map[int64]error{1: failure}}
		items := []*albumItem{fakePhotoItem(1), fakePhotoItem(2)}
		sendAlbumItems(context.Background(), api, &tg.InputPeerSelf{}, nil, items)
		assert.Empty(t, api.albums)
		require.Len(t, api.singles, 1)
		assert.Equal(t, &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: 1002}}, api.singles[0].Media)
		assert.ErrorIs(t, items[0].err, failure)
		assert.Equal(t, 1, sentMessageID(t, items[1]))
	})
}
