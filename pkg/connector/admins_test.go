package connector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestAdminRightsRoundTrip(t *testing.T) {
	// Each level turned into rights must map back to the same level, so a change made from Matrix doesn't
	// come back from Telegram as a different one.
	for _, level := range []*int{otherPowerLevel, deleteMessagesPowerLevel, changeInfoPowerLevel,
		inviteUsersPowerLevel, manageCallPowerLevel, pinMessagesPowerLevel, manageTopicsPowerLevel,
		banUsersPowerLevel, addAdminsPowerLevel} {
		rights, isAdmin := adminRightsForPowerLevel(*level, false)
		assert.True(t, isAdmin)
		assert.Equal(t, *level, *adminRightsToPowerLevel(rights), "level %d", *level)
	}
	for _, level := range []*int{postMessagesPowerLevel, editMessagesPowerLevel} {
		rights, _ := adminRightsForPowerLevel(*level, true)
		assert.Equal(t, *level, *adminRightsToPowerLevel(rights), "broadcast level %d", *level)
	}
	_, isAdmin := adminRightsForPowerLevel(0, false)
	assert.False(t, isAdmin)
	rights, _ := adminRightsForPowerLevel(*addAdminsPowerLevel, false)
	assert.False(t, rights.PostMessages, "supergroups have no posting right")
}

func TestAdminChanged(t *testing.T) {
	change := func(orig, new int) *bridgev2.SinglePowerLevelChange {
		return &bridgev2.SinglePowerLevelChange{OrigLevel: orig, NewLevel: new, NewIsSet: true}
	}
	assert.True(t, adminChanged(change(0, 50)), "made an admin")
	assert.True(t, adminChanged(change(55, 0)), "no longer one")
	assert.True(t, adminChanged(change(50, 60)), "more rights")
	assert.False(t, adminChanged(change(0, -1)), "muting is not an admin change")
	assert.False(t, adminChanged(change(95, 0)), "the creator stays the creator")
	assert.False(t, adminChanged(change(50, 50)))
}

func TestParticipantPowerLevel(t *testing.T) {
	id, level, ok := participantPowerLevel(&tg.ChannelParticipantAdmin{UserID: 7, AdminRights: tg.ChatAdminRights{BanUsers: true}})
	assert.True(t, ok)
	assert.Equal(t, int64(7), id)
	assert.Equal(t, *banUsersPowerLevel, *level)
	_, level, ok = participantPowerLevel(&tg.ChannelParticipant{UserID: 7})
	assert.True(t, ok)
	assert.Equal(t, 0, *level, "a demoted admin")
	_, _, ok = participantPowerLevel(&tg.ChannelParticipantLeft{})
	assert.False(t, ok, "leaving isn't an admin change")
}
