package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

var _ bridgev2.RoomTopicHandlingNetworkAPI = (*TelegramClient)(nil)

// HandleMatrixRoomTopic sets a group's or channel's description, which the chat info bridges as the topic.
func (tc *TelegramClient) HandleMatrixRoomTopic(ctx context.Context, msg *bridgev2.MatrixRoomTopic) (bool, error) {
	peerType, _, topicID, err := ids.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return false, err
	}
	if peerType == ids.PeerTypeUser || topicID > 0 {
		// A private chat has no description, and a forum topic only a title.
		return false, fmt.Errorf("this chat has no description on Telegram")
	}
	if err = tc.clientInitialized.Wait(ctx); err != nil {
		return false, err
	}
	peer, _, err := tc.inputPeerForPortalID(ctx, msg.Portal.ID)
	if err != nil {
		return false, err
	}
	_, err = tc.client.API().MessagesEditChatAbout(ctx, &tg.MessagesEditChatAboutRequest{
		Peer:  peer,
		About: msg.Content.Topic,
	})
	if err != nil && !tg.IsChatAboutNotModified(err) {
		return false, tc.humaniseSendError(err)
	}
	msg.Portal.Topic = msg.Content.Topic
	msg.Portal.TopicSet = true
	return true, nil
}

var _ bridgev2.MarkedUnreadHandlingNetworkAPI = (*TelegramClient)(nil)

// HandleMarkedUnread marks the chat unread (or not) on Telegram when it's marked in Matrix.
func (tc *TelegramClient) HandleMarkedUnread(ctx context.Context, msg *bridgev2.MatrixMarkedUnread) error {
	if err := tc.clientInitialized.Wait(ctx); err != nil {
		return err
	}
	peer, _, err := tc.inputPeerForPortalID(ctx, msg.Portal.ID)
	if err != nil {
		return err
	}
	_, err = tc.client.API().MessagesMarkDialogUnread(ctx, markUnreadRequest(peer, msg.Content.Unread))
	return err
}

func markUnreadRequest(peer tg.InputPeerClass, unread bool) *tg.MessagesMarkDialogUnreadRequest {
	req := &tg.MessagesMarkDialogUnreadRequest{Peer: &tg.InputDialogPeer{Peer: peer}}
	req.SetUnread(unread)
	return req
}

// onDialogUnreadMark marks the Matrix room unread when the chat is marked unread on Telegram.
func (tc *TelegramClient) onDialogUnreadMark(update *tg.UpdateDialogUnreadMark) error {
	dialog, ok := update.Peer.(*tg.DialogPeer)
	if !ok {
		// Folders can be marked unread too; they have no room.
		return nil
	}
	res := tc.main.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.MarkUnread{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMarkUnread,
			PortalKey: tc.makePortalKeyFromPeer(dialog.Peer, 0),
			Sender:    tc.mySender(),
		},
		Unread: update.Unread,
	})
	return resultToError(res)
}
