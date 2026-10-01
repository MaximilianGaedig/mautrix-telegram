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
	"fmt"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/connector/matrixfmt"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

var _ bridgev2.AlbumHandlingNetworkAPI = (*TelegramClient)(nil)

// albumKind is what Telegram lets share an album. Photos and videos mix freely, files only go
// with files and music only with music.
type albumKind int

const (
	// albumKindNone is media that can't be in an album at all: GIFs, voice messages and stickers.
	albumKindNone albumKind = iota
	albumKindVisual
	albumKindDocument
	albumKindAudio
)

// albumKindOf tells which kind of album a freshly uploaded file can go into. It looks at the
// media made by makeUploadedMedia, as that is where it was decided what the file is sent as:
// an image that is too large for a photo, for example, is a document by now.
func albumKindOf(media tg.InputMediaClass) albumKind {
	switch typed := media.(type) {
	case *tg.InputMediaUploadedPhoto:
		return albumKindVisual
	case *tg.InputMediaUploadedDocument:
		if typed.MimeType == "image/gif" {
			return albumKindNone
		}
		kind := albumKindDocument
		for _, attr := range typed.Attributes {
			switch typedAttr := attr.(type) {
			case *tg.DocumentAttributeAnimated, *tg.DocumentAttributeSticker:
				return albumKindNone
			case *tg.DocumentAttributeAudio:
				if typedAttr.Voice {
					return albumKindNone
				}
				kind = albumKindAudio
			case *tg.DocumentAttributeVideo:
				if typedAttr.RoundMessage {
					return albumKindNone
				}
				kind = albumKindVisual
			}
		}
		return kind
	default:
		return albumKindNone
	}
}

// planAlbums splits the items of a Matrix album into the albums Telegram accepts. Items stay in
// their order: an album is a run of neighbours of one kind, at most maxAlbumSize long. The
// result holds the indexes of each group. A group of one is sent as an ordinary message.
func planAlbums(kinds []albumKind) (groups [][]int) {
	for i, kind := range kinds {
		last := len(groups) - 1
		if kind != albumKindNone && last >= 0 && len(groups[last]) < maxAlbumSize && kinds[groups[last][0]] == kind {
			groups[last] = append(groups[last], i)
		} else {
			groups = append(groups, []int{i})
		}
	}
	return groups
}

// albumItem is one Matrix message of an album on its way to Telegram.
type albumItem struct {
	msg      *bridgev2.MatrixMessage
	media    tg.InputMediaClass
	randomID int64
	message  string
	entities []tg.MessageEntityClass

	// Exactly one of these is set once the item has been through sendAlbumItems.
	updates tg.UpdatesClass
	err     error
}

// albumAPI is the part of the Telegram API that sending an album needs. It exists so that the
// splitting and the bookkeeping can be tested without Telegram.
type albumAPI interface {
	MessagesUploadMedia(ctx context.Context, request *tg.MessagesUploadMediaRequest) (tg.MessageMediaClass, error)
	MessagesSendMedia(ctx context.Context, request *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error)
	MessagesSendMultiMedia(ctx context.Context, request *tg.MessagesSendMultiMediaRequest) (tg.UpdatesClass, error)
}

// savedAlbumMedia turns the answer of messages.uploadMedia into media that can be sent.
// messages.sendMultiMedia doesn't take freshly uploaded files, only photos and documents that
// Telegram already knows, which is what messages.uploadMedia is for.
func savedAlbumMedia(uploaded tg.MessageMediaClass, spoiler bool) (tg.InputMediaClass, error) {
	switch typed := uploaded.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := typed.Photo.(*tg.Photo)
		if !ok {
			return nil, fmt.Errorf("unexpected uploaded photo type %T", typed.Photo)
		}
		return &tg.InputMediaPhoto{ID: photo.AsInput(), Spoiler: spoiler}, nil
	case *tg.MessageMediaDocument:
		doc, ok := typed.Document.(*tg.Document)
		if !ok {
			return nil, fmt.Errorf("unexpected uploaded document type %T", typed.Document)
		}
		return &tg.InputMediaDocument{ID: doc.AsInput(), Spoiler: spoiler}, nil
	default:
		return nil, fmt.Errorf("unexpected uploaded media type %T", uploaded)
	}
}

func uploadedMediaSpoiler(media tg.InputMediaClass) bool {
	switch typed := media.(type) {
	case *tg.InputMediaUploadedPhoto:
		return typed.Spoiler
	case *tg.InputMediaUploadedDocument:
		return typed.Spoiler
	default:
		return false
	}
}

// makeMultiMedia builds the items of a messages.sendMultiMedia request. Every item keeps its own
// caption. A client puts the caption of an album on its first item, and Telegram shows the
// caption of an album's only captioned item as the caption of the whole album.
func makeMultiMedia(items []*albumItem) []tg.InputSingleMedia {
	multiMedia := make([]tg.InputSingleMedia, len(items))
	for i, item := range items {
		multiMedia[i] = tg.InputSingleMedia{
			Media:    item.media,
			RandomID: item.randomID,
			Message:  item.message,
			Entities: item.entities,
		}
	}
	return multiMedia
}

// splitAlbumUpdates takes the answer to messages.sendMultiMedia apart into what the answer to
// sending each item on its own would have been, in the order of the given random IDs. Telegram
// answers with one updateMessageID per item, which is the only link between what was sent and
// the message it became. An item that has none is nil in the result.
func splitAlbumUpdates(updates tg.UpdatesClass, randomIDs []int64) ([]*tg.Updates, error) {
	combined, ok := updates.(*tg.Updates)
	if !ok {
		return nil, fmt.Errorf("unknown update from album response %T", updates)
	}
	messageIDs := make(map[int64]*tg.UpdateMessageID, len(randomIDs))
	messages := make(map[int]tg.UpdateClass, len(randomIDs))
	for _, u := range combined.Updates {
		switch update := u.(type) {
		case *tg.UpdateMessageID:
			messageIDs[update.RandomID] = update
		case *tg.UpdateNewMessage:
			if msg, ok := update.Message.(*tg.Message); ok {
				messages[msg.ID] = update
			}
		case *tg.UpdateNewChannelMessage:
			if msg, ok := update.Message.(*tg.Message); ok {
				messages[msg.ID] = update
			}
		}
	}
	split := make([]*tg.Updates, len(randomIDs))
	for i, randomID := range randomIDs {
		messageID, ok := messageIDs[randomID]
		if !ok {
			continue
		}
		split[i] = &tg.Updates{Updates: []tg.UpdateClass{messageID}, Date: combined.Date}
		if message, ok := messages[messageID.ID]; ok {
			split[i].Updates = append(split[i].Updates, message)
		}
	}
	return split, nil
}

// sendAlbumItems sends the items to Telegram in as few albums as Telegram allows, and leaves on
// every item either the updates for its message or the reason it wasn't sent. A failure only
// affects the album it happens in, so the items of the albums before it stay sent.
func sendAlbumItems(ctx context.Context, api albumAPI, peer tg.InputPeerClass, replyTo tg.InputReplyToClass, items []*albumItem) {
	kinds := make([]albumKind, len(items))
	for i, item := range items {
		kinds[i] = albumKindOf(item.media)
	}
	for _, group := range planAlbums(kinds) {
		album := make([]*albumItem, 0, len(group))
		for _, i := range group {
			album = append(album, items[i])
		}
		if len(album) > 1 {
			album = saveAlbumMedia(ctx, api, peer, album)
		}
		switch len(album) {
		case 0:
		case 1:
			album[0].updates, album[0].err = api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
				Peer:     peer,
				Message:  album[0].message,
				Entities: album[0].entities,
				Media:    album[0].media,
				ReplyTo:  replyTo,
				RandomID: album[0].randomID,
			})
		default:
			sendOneAlbum(ctx, api, peer, replyTo, album)
		}
	}
}

// saveAlbumMedia makes Telegram keep the uploaded file of every item, and returns the items it
// worked for. The others are left out of the album with their error.
func saveAlbumMedia(ctx context.Context, api albumAPI, peer tg.InputPeerClass, album []*albumItem) (saved []*albumItem) {
	for _, item := range album {
		uploaded, err := api.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{Peer: peer, Media: item.media})
		if err == nil {
			item.media, err = savedAlbumMedia(uploaded, uploadedMediaSpoiler(item.media))
		}
		if err != nil {
			item.err = fmt.Errorf("failed to prepare media for album: %w", err)
			continue
		}
		saved = append(saved, item)
	}
	return saved
}

func sendOneAlbum(ctx context.Context, api albumAPI, peer tg.InputPeerClass, replyTo tg.InputReplyToClass, album []*albumItem) {
	updates, err := api.MessagesSendMultiMedia(ctx, &tg.MessagesSendMultiMediaRequest{
		Peer:       peer,
		ReplyTo:    replyTo,
		MultiMedia: makeMultiMedia(album),
	})
	var split []*tg.Updates
	if err == nil {
		randomIDs := make([]int64, len(album))
		for i, item := range album {
			randomIDs[i] = item.randomID
		}
		split, err = splitAlbumUpdates(updates, randomIDs)
	}
	for i, item := range album {
		if err != nil {
			item.err = err
		} else if split[i] == nil {
			item.err = fmt.Errorf("couldn't find update message ID update")
		} else {
			item.updates = split[i]
		}
	}
}

// albumGroupedID finds the album Telegram put a sent message into.
func albumGroupedID(updates tg.UpdatesClass) int64 {
	combined, ok := updates.(*tg.Updates)
	if !ok {
		return 0
	}
	for _, u := range combined.Updates {
		var message tg.MessageClass
		switch update := u.(type) {
		case *tg.UpdateNewMessage:
			message = update.Message
		case *tg.UpdateNewChannelMessage:
			message = update.Message
		}
		if msg, ok := message.(*tg.Message); ok && msg.GroupedID != 0 {
			return msg.GroupedID
		}
	}
	return 0
}

// HandleMatrixAlbum sends media messages that belong together on Matrix as one album on Telegram.
//
// Telegram may need more than one album for them: an album holds ten items, and it can't mix
// photos and videos with files. What can't be in any album is sent on its own, in its place.
func (tc *TelegramClient) HandleMatrixAlbum(ctx context.Context, msgs []*bridgev2.MatrixMessage) ([]bridgev2.MatrixAlbumPartResult, error) {
	tc.markActive(ctx)
	portal := msgs[0].Portal
	if portal.RoomType == database.RoomTypeSpace {
		return nil, fmt.Errorf("can't send messages to space portals")
	}
	// Handle Matrix events only after initial connection has been established to avoid deadlocking gotd
	err := tc.clientInitialized.Wait(ctx)
	if err != nil {
		return nil, err
	}
	peer, topicID, err := tc.inputPeerForPortalID(ctx, portal.ID)
	if err != nil {
		return nil, err
	}
	log := zerolog.Ctx(ctx).With().
		Stringer("portal_key", portal.PortalKey).
		Any("peer_id", peer).
		Logger()
	ctx = log.WithContext(ctx)

	// An album is one reply on Telegram, so the first item that replies to something decides.
	var replyTo tg.InputReplyToClass
	for _, msg := range msgs {
		if msg.ReplyTo == nil {
			continue
		}
		_, messageID, err := ids.ParseMessageID(msg.ReplyTo.ID)
		if err != nil {
			log.Warn().Msg("failed to parse replied-to message ID")
			return nil, err
		}
		replyTo = &tg.InputReplyToMessage{ReplyToMsgID: messageID}
		break
	}
	if topicID > 0 {
		if replyTo == nil {
			replyTo = &tg.InputReplyToMessage{ReplyToMsgID: topicID}
		} else {
			replyTo.(*tg.InputReplyToMessage).TopMsgID = topicID
		}
	}

	results := make([]bridgev2.MatrixAlbumPartResult, len(msgs))
	items := make([]*albumItem, 0, len(msgs))
	resultIndex := make(map[*albumItem]int, len(msgs))
	maxLength := tc.getMaxMessageLength(ctx, true)
	for i, msg := range msgs {
		switch msg.Content.MsgType {
		case event.MsgImage, event.MsgFile, event.MsgAudio, event.MsgVideo:
		default:
			results[i].Err = fmt.Errorf("unsupported message type %s in album", msg.Content.MsgType)
			continue
		}
		item := &albumItem{msg: msg, randomID: parseRandomID(msg.InputTransactionID)}
		item.message, item.entities = matrixfmt.Parse(ctx, tc.matrixParser, msg.Content, msg.Portal, maxLength)
		forceDocument, _ := msg.Event.Content.Raw["fi.mau.telegram.force_document"].(bool)
		item.media, err = tc.transferMediaToTelegram(ctx, msg.Content, false, false, forceDocument, matrixMediaHasSpoiler(msg.Event.Content.Raw))
		if err != nil {
			// A file that can't be transferred only takes itself out of the album.
			log.Err(err).Stringer("event_id", msg.Event.ID).Msg("failed to transfer album item to Telegram")
			results[i].Err = err
			continue
		}
		resultIndex[item] = i
		items = append(items, item)
	}

	sendAlbumItems(ctx, tc.client.API(), peer, replyTo, items)

	for _, item := range items {
		result := &results[resultIndex[item]]
		if item.err != nil {
			log.Err(item.err).Stringer("event_id", item.msg.Event.ID).Msg("failed to send album item to Telegram")
			result.Err = tc.humaniseSendError(item.err)
			continue
		}
		result.Response, result.Err = tc.makeSendResponse(ctx, item.msg, item.updates, item.randomID, "", item.msg.Content.Body)
		if result.Err == nil {
			// The same field is set when an album comes in from Telegram.
			result.Response.DB.Metadata.(*MessageMetadata).GroupedID = albumGroupedID(item.updates)
		}
	}
	return results, nil
}
