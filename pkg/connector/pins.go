package connector

import (
	"context"
	"fmt"
	"slices"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tgerr"
)

var _ bridgev2.PinHandlingNetworkAPI = (*TelegramClient)(nil)

// The most pinned messages fetched for one chat. Telegram shows every pin in the chat header's list, and a
// chat with more than this is rare enough that the rest can wait for their next pin update.
const maxPinnedMessages = 100

// pinnedMessages is every message pinned in the chat, oldest pin first, as bridge message IDs.
func (tc *TelegramClient) pinnedMessages(ctx context.Context, portalID networkid.PortalID) ([]networkid.MessageID, error) {
	peer, topicID, err := tc.inputPeerForPortalID(ctx, portalID)
	if err != nil {
		return nil, err
	}
	req := &tg.MessagesSearchRequest{
		Peer:   peer,
		Filter: &tg.InputMessagesFilterPinned{},
		Limit:  maxPinnedMessages,
	}
	if topicID > 0 {
		req.SetTopMsgID(topicID)
	}
	var resp tg.MessagesMessagesClass
	retry := true
	for attempts := 0; retry && attempts < 5; attempts++ {
		resp, err = tc.client.API().MessagesSearch(ctx, req)
		retry, err = tgerr.FloodWait(ctx, err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to search pinned messages: %w", err)
	}
	modified, ok := resp.AsModified()
	if !ok {
		return nil, fmt.Errorf("unexpected pinned messages response %T", resp)
	}
	var pinned []networkid.MessageID
	for _, msg := range modified.GetMessages() {
		if _, empty := msg.(*tg.MessageEmpty); empty {
			continue
		}
		pinned = append(pinned, ids.MakeMessageID(peerFromInput(peer), msg.GetID()))
	}
	// Telegram lists the newest first; the room's pins are in the order they were pinned.
	slices.Reverse(pinned)
	return pinned, nil
}

// peerFromInput is the chat ID MakeMessageID needs for a peer: only channels scope message IDs.
func peerFromInput(peer tg.InputPeerClass) any {
	if channel, ok := peer.(*tg.InputPeerChannel); ok {
		return channel.ChannelID
	}
	return nil
}

// onPinnedMessages brings a chat's pins up to date after Telegram says some were pinned or unpinned. The
// update only names the messages that changed, so the whole list is fetched rather than patched.
func (tc *TelegramClient) onPinnedMessages(ctx context.Context, portalKey networkid.PortalKey) error {
	pinned, err := tc.pinnedMessages(ctx, portalKey.ID)
	if err != nil {
		return err
	}
	res := tc.main.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{ChatInfo: &bridgev2.ChatInfo{PinnedMessages: &pinned}},
	})
	return resultToError(res)
}

// HandleMatrixPin pins or unpins a message on Telegram when it is pinned or unpinned in the portal room.
func (tc *TelegramClient) HandleMatrixPin(ctx context.Context, msg *bridgev2.MatrixPin) error {
	peer, _, err := tc.inputPeerForPortalID(ctx, msg.Portal.ID)
	if err != nil {
		return err
	}
	_, messageID, err := ids.ParseMessageID(msg.TargetMessage.ID)
	if err != nil {
		return err
	}
	// The pin's own update comes back through the update stream like any other, which is what puts it in
	// the room's pins; nothing more to do with the response here.
	_, err = tc.client.API().MessagesUpdatePinnedMessage(ctx, &tg.MessagesUpdatePinnedMessageRequest{
		Peer:   peer,
		ID:     messageID,
		Unpin:  !msg.Pinned,
		Silent: true,
	})
	return err
}
