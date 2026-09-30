package connector

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

var _ bridgev2.PowerLevelHandlingNetworkAPI = (*TelegramClient)(nil)

// adminRightsForPowerLevel turns a power level back into admin rights, the other way round from
// adminRightsToPowerLevel: that maps an admin to the level of their strongest right, so a level here grants
// its right and every weaker one. Posting and editing messages exist only in broadcast channels. Below the
// "other" level there are no rights: the person isn't an admin.
func adminRightsForPowerLevel(level int, broadcast bool) (rights tg.ChatAdminRights, isAdmin bool) {
	if level < *otherPowerLevel {
		return rights, false
	}
	rights.Other = true
	rights.PostMessages = broadcast && level >= *postMessagesPowerLevel
	rights.EditMessages = broadcast && level >= *editMessagesPowerLevel
	rights.DeleteMessages = level >= *deleteMessagesPowerLevel
	rights.ChangeInfo = level >= *changeInfoPowerLevel
	rights.InviteUsers = level >= *inviteUsersPowerLevel
	rights.ManageCall = level >= *manageCallPowerLevel
	rights.PinMessages = level >= *pinMessagesPowerLevel
	rights.ManageTopics = !broadcast && level >= *manageTopicsPowerLevel
	rights.BanUsers = level >= *banUsersPowerLevel
	rights.AddAdmins = level >= *addAdminsPowerLevel
	return rights, true
}

// adminChanged reports whether a power level change changes someone's admin rights. The creator's can't be
// changed, and muting (a negative level) is about banned rights, not admin ones.
func adminChanged(change *bridgev2.SinglePowerLevelChange) bool {
	if change.OrigLevel == change.NewLevel || change.OrigLevel >= *creatorPowerLevel {
		return false
	}
	return change.OrigLevel >= *otherPowerLevel || change.NewLevel >= *otherPowerLevel
}

// HandleMatrixPowerLevels makes people admins of a group or channel, changes their admin rights, or takes
// them away, from the room's power levels.
func (tc *TelegramClient) HandleMatrixPowerLevels(ctx context.Context, msg *bridgev2.MatrixPowerLevelChange) (bool, error) {
	peerType, chatID, _, err := ids.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return false, err
	} else if peerType == ids.PeerTypeUser {
		return false, nil
	}
	if err = tc.clientInitialized.Wait(ctx); err != nil {
		return false, err
	}
	broadcast := peerType == ids.PeerTypeChannel && !msg.Portal.Metadata.(*PortalMetadata).IsSuperGroup
	changed := false
	for _, change := range msg.Users {
		if !adminChanged(&change.SinglePowerLevelChange) {
			continue
		}
		var userID int64
		switch target := change.Target.(type) {
		case *bridgev2.Ghost:
			var targetType ids.PeerType
			targetType, userID, err = ids.ParseUserID(target.ID)
			if err == nil && targetType != ids.PeerTypeUser {
				continue
			}
		case *bridgev2.UserLogin:
			userID, err = ids.ParseUserLoginID(target.ID)
		default:
			continue
		}
		if err != nil {
			return changed, err
		}
		inputUser, err := tc.getInputUser(ctx, userID)
		if err != nil {
			return changed, err
		}
		rights, isAdmin := adminRightsForPowerLevel(change.NewLevel, broadcast)
		switch peerType {
		case ids.PeerTypeChat:
			_, err = tc.client.API().MessagesEditChatAdmin(ctx, &tg.MessagesEditChatAdminRequest{
				ChatID: chatID, UserID: inputUser, IsAdmin: isAdmin,
			})
		case ids.PeerTypeChannel:
			var inputChannel tg.InputChannelClass
			if inputChannel, err = tc.getInputChannel(ctx, chatID); err == nil {
				_, err = tc.client.API().ChannelsEditAdmin(ctx, &tg.ChannelsEditAdminRequest{
					Channel: inputChannel, UserID: inputUser, AdminRights: rights,
				})
			}
		}
		if err != nil {
			return changed, fmt.Errorf("failed to change admin rights: %w", tc.humaniseSendError(err))
		}
		changed = true
	}
	return changed, nil
}

// participantPowerLevel is the power level a channel member has, for the kinds of participant that are
// members; the others (left, banned) aren't admin changes.
func participantPowerLevel(participant tg.ChannelParticipantClass) (userID int64, level *int, ok bool) {
	switch p := participant.(type) {
	case *tg.ChannelParticipant:
		return p.UserID, anyonePowerLevel, true
	case *tg.ChannelParticipantSelf:
		return p.UserID, anyonePowerLevel, true
	case *tg.ChannelParticipantAdmin:
		return p.UserID, adminRightsToPowerLevel(p.AdminRights), true
	case *tg.ChannelParticipantCreator:
		return p.UserID, creatorPowerLevel, true
	}
	return 0, nil, false
}

func (tc *TelegramClient) queueMemberPowerLevel(portalKey networkid.PortalKey, userID int64, level *int, tgEvent string) error {
	sender := tc.senderForUserID(userID)
	res := tc.main.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatInfoChange,
			PortalKey: portalKey,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("tg_event", tgEvent).Int64("user_id", userID)
			},
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			MemberChanges: &bridgev2.ChatMemberList{
				MemberMap: bridgev2.ChatMemberMap{sender.Sender: {EventSender: sender, PowerLevel: level}},
			},
		},
	})
	return resultToError(res)
}

// onChannelParticipant bridges a supergroup or channel admin being made, changed or unmade as it happens,
// instead of at the next resync.
func (tc *TelegramClient) onChannelParticipant(ctx context.Context, update *tg.UpdateChannelParticipant) error {
	newParticipant, ok := update.GetNewParticipant()
	if !ok {
		return nil
	}
	userID, level, ok := participantPowerLevel(newParticipant)
	if !ok {
		return nil
	}
	return tc.queueMemberPowerLevel(tc.makePortalKeyFromID(ids.PeerTypeChannel, update.ChannelID, 0), userID, level, "updateChannelParticipant")
}

// onChatParticipantAdmin is the same for a basic group, whose admins are all equal.
func (tc *TelegramClient) onChatParticipantAdmin(ctx context.Context, update *tg.UpdateChatParticipantAdmin) error {
	level := ptr.Ptr(0)
	if update.IsAdmin {
		level = modPowerLevel
	}
	return tc.queueMemberPowerLevel(tc.makePortalKeyFromID(ids.PeerTypeChat, update.ChatID, 0), update.UserID, level, "updateChatParticipantAdmin")
}
