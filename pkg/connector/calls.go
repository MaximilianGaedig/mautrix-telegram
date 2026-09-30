package connector

import (
	"strconv"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/calllog"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// Telegram reports a call twice: live (updatePhoneCall, when it rings) and afterwards as a
// messageActionPhoneCall service message with the reason and duration. The live ring is kept as one timeline
// message through calllog, and the service message finishes that message, so a call is one line that changes
// from "Incoming voice call" to "Missed voice call" or "Voice call, 3:12" instead of two lines. The service
// message on its own (calls from before the bridge, calls that never rang here, your own calls) is worded by
// the same calllog texts.

// callFromAction is the calllog record of a call as far as the service message says: the state it ended in and
// how long it was connected.
func callFromAction(action *tg.MessageActionPhoneCall, outgoing bool, portal networkid.PortalKey, caller bridgev2.EventSender, at time.Time) *calllog.Call {
	call := &calllog.Call{
		Portal:   portal,
		Caller:   caller,
		Video:    action.Video,
		Outgoing: outgoing,
		Started:  at,
		Finished: at,
	}
	duration := time.Duration(action.Duration) * time.Second
	switch action.Reason.(type) {
	case *tg.PhoneCallDiscardReasonMissed:
		call.State = calllog.Missed
	case *tg.PhoneCallDiscardReasonBusy:
		call.State = calllog.Declined
	default:
		// Hangup, disconnect, moved to a conference call, or no reason at all: it was answered if it lasted.
		call.State = calllog.Ended
		if duration > 0 {
			call.Answered = at.Add(-duration)
		}
	}
	return call
}

// phoneCallID is the calllog ID of a Telegram call.
func phoneCallID(callID int64) string {
	return strconv.FormatInt(callID, 10)
}

// finishLoggedCall ends a call that rang through the log, so that its message is edited to what the service
// message says. It returns nil when the log doesn't know the call.
func finishLoggedCall(log *calllog.Log, action *tg.MessageActionPhoneCall, at time.Time) bridgev2.RemoteEvent {
	id := phoneCallID(action.CallID)
	switch action.Reason.(type) {
	case *tg.PhoneCallDiscardReasonBusy:
		return log.Decline(id, at)
	case *tg.PhoneCallDiscardReasonMissed:
		return log.End(id, at)
	}
	if action.Duration > 0 {
		// The edit for the answer is not sent on its own; the one for the end shows both.
		log.Answer(id, at.Add(-time.Duration(action.Duration)*time.Second))
	}
	return log.End(id, at)
}
