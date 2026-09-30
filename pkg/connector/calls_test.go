package connector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/calllog"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

var callAt = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func TestCallTexts(t *testing.T) {
	tests := []struct {
		name     string
		action   *tg.MessageActionPhoneCall
		outgoing bool
		want     string
	}{
		{"missed", &tg.MessageActionPhoneCall{Reason: &tg.PhoneCallDiscardReasonMissed{}}, false, "Missed voice call"},
		{"missed video", &tg.MessageActionPhoneCall{Video: true, Reason: &tg.PhoneCallDiscardReasonMissed{}}, false, "Missed video call"},
		{"declined", &tg.MessageActionPhoneCall{Reason: &tg.PhoneCallDiscardReasonBusy{}}, false, "Declined voice call"},
		{"declined video", &tg.MessageActionPhoneCall{Video: true, Reason: &tg.PhoneCallDiscardReasonBusy{}}, false, "Declined video call"},
		{"answered", &tg.MessageActionPhoneCall{Reason: &tg.PhoneCallDiscardReasonHangup{}, Duration: 192}, false, "Voice call, 3:12"},
		{"answered video", &tg.MessageActionPhoneCall{Video: true, Reason: &tg.PhoneCallDiscardReasonHangup{}, Duration: 45}, true, "Video call, 0:45"},
		{"answered long", &tg.MessageActionPhoneCall{Reason: &tg.PhoneCallDiscardReasonDisconnect{}, Duration: 3725}, false, "Voice call, 1:02:05"},
		{"own call nobody answered", &tg.MessageActionPhoneCall{Reason: &tg.PhoneCallDiscardReasonMissed{}}, true, "Cancelled voice call"},
		{"own video call nobody answered", &tg.MessageActionPhoneCall{Video: true, Reason: &tg.PhoneCallDiscardReasonMissed{}}, true, "Cancelled video call"},
		{"own call declined", &tg.MessageActionPhoneCall{Reason: &tg.PhoneCallDiscardReasonBusy{}}, true, "Declined voice call"},
		{"hangup without duration", &tg.MessageActionPhoneCall{Reason: &tg.PhoneCallDiscardReasonHangup{}}, false, "Voice call ended"},
		{"no reason", &tg.MessageActionPhoneCall{Duration: 10}, false, "Voice call, 0:10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			call := callFromAction(tt.action, tt.outgoing, networkid.PortalKey{}, bridgev2.EventSender{}, callAt)
			assert.Equal(t, tt.want, call.Text())
		})
	}
}

func ringingCall(log *calllog.Log, video bool) {
	log.Start("77", calllog.Call{Portal: networkid.PortalKey{ID: "1"}, Video: video, Started: callAt.Add(-30 * time.Second)})
}

func finalText(t *testing.T, evt bridgev2.RemoteEvent) string {
	t.Helper()
	require.NotNil(t, evt)
	msg, ok := evt.(*simplevent.Message[*calllog.Call])
	require.True(t, ok, "%T", evt)
	assert.Equal(t, bridgev2.RemoteEventEdit, msg.GetType(), "the service message must edit the ringing message")
	assert.Equal(t, calllog.MessageID("77"), msg.TargetMessage)
	return msg.Data.Text()
}

func TestFinishLoggedCall(t *testing.T) {
	tests := []struct {
		name   string
		video  bool
		action *tg.MessageActionPhoneCall
		want   string
	}{
		{"missed", false, &tg.MessageActionPhoneCall{CallID: 77, Reason: &tg.PhoneCallDiscardReasonMissed{}}, "Missed voice call"},
		{"declined", true, &tg.MessageActionPhoneCall{CallID: 77, Video: true, Reason: &tg.PhoneCallDiscardReasonBusy{}}, "Declined video call"},
		{"answered", false, &tg.MessageActionPhoneCall{CallID: 77, Reason: &tg.PhoneCallDiscardReasonHangup{}, Duration: 192}, "Voice call, 3:12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := calllog.New()
			ringingCall(log, tt.video)
			assert.Equal(t, tt.want, finalText(t, finishLoggedCall(log, tt.action, callAt)))
		})
	}
	t.Run("unknown call is left to the service message", func(t *testing.T) {
		evt := finishLoggedCall(calllog.New(), &tg.MessageActionPhoneCall{CallID: 99, Reason: &tg.PhoneCallDiscardReasonMissed{}}, callAt)
		assert.Nil(t, evt)
	})
}
