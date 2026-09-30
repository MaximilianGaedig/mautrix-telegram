package connector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func testServiceEnv() serviceTextEnv {
	return serviceTextEnv{peerName: func(p tg.PeerClass) string {
		switch p := p.(type) {
		case *tg.PeerUser:
			return map[int64]string{1: "Alice", 2: "Bob"}[p.UserID]
		case *tg.PeerChannel:
			return "Test Channel"
		}
		return "someone"
	}}
}

func TestServiceMessageText(t *testing.T) {
	tests := []struct {
		name   string
		action tg.MessageActionClass
		want   string
	}{
		{"screenshot", &tg.MessageActionScreenshotTaken{}, "Took a screenshot"},
		{"sign up", &tg.MessageActionContactSignUp{}, "Joined Telegram"},
		{"join request", &tg.MessageActionChatJoinedByRequest{}, "Joined by request"},
		{"history clear", &tg.MessageActionHistoryClear{}, "Cleared the chat history"},
		{"custom", &tg.MessageActionCustomAction{Message: "Hello there"}, "Hello there"},
		{"game", &tg.MessageActionGameScore{Score: 42}, "Scored 42 in a game"},
		{"proximity metres", &tg.MessageActionGeoProximityReached{FromID: &tg.PeerUser{UserID: 1}, ToID: &tg.PeerUser{UserID: 2}, Distance: 250}, "Alice is now within 250 m of Bob"},
		{"proximity km", &tg.MessageActionGeoProximityReached{FromID: &tg.PeerUser{UserID: 1}, ToID: &tg.PeerUser{UserID: 2}, Distance: 1500}, "Alice is now within 1.5 km of Bob"},
		{"boost one", &tg.MessageActionBoostApply{Boosts: 1}, "Boosted this chat"},
		{"boost many", &tg.MessageActionBoostApply{Boosts: 3}, "Boosted this chat 3 times"},
		{"bot allowed domain", &tg.MessageActionBotAllowed{Domain: "example.test"}, "Allowed the bot to message them by logging in on example.test"},
		{"bot allowed menu", &tg.MessageActionBotAllowed{AttachMenu: true}, "Allowed the bot to message them from the attachment menu"},
		{"owner", &tg.MessageActionChangeCreator{NewCreatorID: 2}, "Transferred ownership to Bob"},
		{"owner pending", &tg.MessageActionNewCreatorPending{NewCreatorID: 2}, "Started transferring ownership to Bob"},
		{"conference missed", &tg.MessageActionConferenceCall{Missed: true}, "Missed conference call"},
		{"conference video active", &tg.MessageActionConferenceCall{Active: true, Video: true, OtherParticipants: []tg.PeerClass{&tg.PeerUser{UserID: 1}, &tg.PeerUser{UserID: 2}}}, "Started a video conference call with Alice, Bob"},
		{"conference ended", &tg.MessageActionConferenceCall{Duration: 3725}, "Conference call ended (1:02:05)"},
		{"theme", &tg.MessageActionSetChatTheme{Theme: &tg.ChatTheme{Emoticon: "🎃"}}, "Changed the chat theme to 🎃"},
		{"theme off", &tg.MessageActionSetChatTheme{Theme: &tg.ChatTheme{}}, "Turned off the chat theme"},
		{"theme gift", &tg.MessageActionSetChatTheme{Theme: &tg.ChatThemeUniqueGift{Gift: &tg.StarGiftUnique{Title: "Plush", Num: 7}}}, "Changed the chat theme to Plush #7"},
		{"wallpaper", &tg.MessageActionSetChatWallPaper{}, "Changed the chat wallpaper"},
		{"wallpaper both", &tg.MessageActionSetChatWallPaper{ForBoth: true}, "Set a new wallpaper for both of you"},
		{"wallpaper same", &tg.MessageActionSetChatWallPaper{Same: true}, "Set the same wallpaper for this chat"},
		{"protect on", &tg.MessageActionNoForwardsToggle{NewValue: true}, "Enabled content protection (forwarding and saving are restricted)"},
		{"protect off", &tg.MessageActionNoForwardsToggle{PrevValue: true}, "Disabled content protection"},
		{"protect request expired", &tg.MessageActionNoForwardsRequest{NewValue: true, Expired: true}, "The request to restrict forwarding and saving expired"},
		{"poll add", &tg.MessageActionPollAppendAnswer{Answer: &tg.PollAnswer{Text: tg.TextWithEntities{Text: "Maybe"}}}, "Added the option “Maybe” to the poll"},
		{"poll remove", &tg.MessageActionPollDeleteAnswer{Answer: &tg.PollAnswer{Text: tg.TextWithEntities{Text: "Maybe"}}}, "Removed the option “Maybe” from the poll"},
		{"todo add", &tg.MessageActionTodoAppendTasks{List: []tg.TodoItem{{ID: 1, Title: tg.TextWithEntities{Text: "Milk"}}, {ID: 2, Title: tg.TextWithEntities{Text: "Eggs"}}}}, "Added 2 tasks to the checklist: “Milk”, “Eggs”"},
		{"todo add one", &tg.MessageActionTodoAppendTasks{List: []tg.TodoItem{{ID: 1, Title: tg.TextWithEntities{Text: "Milk"}}}}, "Added 1 task to the checklist: “Milk”"},
		{"todo done", &tg.MessageActionTodoCompletions{Completed: []int{1, 2}, Incompleted: []int{3}}, "Marked 2 checklist items as done, Marked 1 checklist item as not done"},
		{"profile photo", &tg.MessageActionSuggestProfilePhoto{}, "Suggested a profile photo"},
		{"birthday", &tg.MessageActionSuggestBirthday{Birthday: tg.Birthday{Day: 3, Month: 11, Year: 1990}}, "Suggested a birthday: 3 November 1990"},
		{"birthday no year", &tg.MessageActionSuggestBirthday{Birthday: tg.Birthday{Day: 3, Month: 11}}, "Suggested a birthday: 3 November"},
		{"shared peers", &tg.MessageActionRequestedPeer{Peers: []tg.PeerClass{&tg.PeerUser{UserID: 1}, &tg.PeerChannel{ChannelID: 5}}}, "Shared Alice, Test Channel"},
		{"shared peers sent me", &tg.MessageActionRequestedPeerSentMe{Peers: []tg.RequestedPeerClass{&tg.RequestedPeerUser{FirstName: "Carol", LastName: "Test"}, &tg.RequestedPeerChat{Title: "Book club"}}}, "Shared Carol Test, Book club"},
		{"passport", &tg.MessageActionSecureValuesSent{Types: []tg.SecureValueTypeClass{&tg.SecureValueTypePersonalDetails{}, &tg.SecureValueTypePhone{}}}, "Shared Telegram Passport data: personal details, phone number"},
		{"web view", &tg.MessageActionWebViewDataSent{Text: "Order"}, "Sent data from the mini app button “Order”"},
		{"payment", &tg.MessageActionPaymentSent{Currency: "USD", TotalAmount: 1299}, "Paid 12.99 USD"},
		{"payment received sub", &tg.MessageActionPaymentSentMe{Currency: "EUR", TotalAmount: 500, RecurringInit: true}, "Received a payment of 5.00 EUR (started a subscription)"},
		{"payment stars", &tg.MessageActionPaymentSent{Currency: "XTR", TotalAmount: 50}, "Paid 50 Stars"},
		{"payment refunded", &tg.MessageActionPaymentRefunded{Peer: &tg.PeerUser{UserID: 2}, Currency: "JPY", TotalAmount: 800}, "Refunded 800 JPY to Bob"},
		{"paid price", &tg.MessageActionPaidMessagesPrice{Stars: 10}, "Set the price of a message to 10 Stars"},
		{"paid free", &tg.MessageActionPaidMessagesPrice{}, "Made messages free"},
		{"paid refund", &tg.MessageActionPaidMessagesRefunded{Count: 1, Stars: 1}, "Refunded 1 paid message (1 Star)"},
		{"premium gift", &tg.MessageActionGiftPremium{Days: 90, Currency: "USD", Amount: 1499, Message: tg.TextWithEntities{Text: "Enjoy"}}, "Gifted Telegram Premium for 3 months (14.99 USD)\n“Enjoy”"},
		{"gift code giveaway", &tg.MessageActionGiftCode{ViaGiveaway: true, Days: 365, BoostPeer: &tg.PeerChannel{ChannelID: 5}}, "Won a Telegram Premium gift code for 1 year in a giveaway from Test Channel"},
		{"gift code unclaimed", &tg.MessageActionGiftCode{ViaGiveaway: true, Unclaimed: true, Days: 30}, "Unclaimed giveaway prize: a Telegram Premium gift code for 1 month"},
		{"gift stars", &tg.MessageActionGiftStars{Stars: 500, Currency: "USD", Amount: 999}, "Gifted 500 Stars (9.99 USD)"},
		{"gift ton", &tg.MessageActionGiftTon{CryptoCurrency: "TON", CryptoAmount: 1_500_000_000}, "Gifted 1.5 TON"},
		{"prize stars", &tg.MessageActionPrizeStars{Stars: 100}, "Won 100 Stars in a giveaway"},
		{"giveaway launch", &tg.MessageActionGiveawayLaunch{Stars: 1000}, "Started a giveaway of 1000 Stars"},
		{"giveaway launch plain", &tg.MessageActionGiveawayLaunch{}, "Started a giveaway"},
		{"giveaway results", &tg.MessageActionGiveawayResults{WinnersCount: 5, UnclaimedCount: 1}, "Giveaway ended with 5 winners (1 unclaimed)"},
		{"giveaway none", &tg.MessageActionGiveawayResults{Stars: true}, "Stars giveaway ended with no winners"},
		{"star gift", &tg.MessageActionStarGift{Gift: &tg.StarGift{Title: "Teddy", Stars: 25}, Message: tg.TextWithEntities{Text: "Hi"}}, "Sent a gift: Teddy (25 Stars)\n“Hi”"},
		{"star gift converted", &tg.MessageActionStarGift{Gift: &tg.StarGift{Title: "Teddy"}, Converted: true, ConvertStars: 20}, "Converted the gift Teddy to 20 Stars"},
		{"star gift unique", &tg.MessageActionStarGiftUnique{Gift: &tg.StarGiftUnique{Title: "Plush", Num: 7}, Transferred: true}, "Transferred a collectible gift: Plush #7"},
		{"star gift upgrade", &tg.MessageActionStarGiftUnique{Gift: &tg.StarGiftUnique{Title: "Plush", Num: 7}, Upgrade: true}, "Upgraded a gift to a collectible: Plush #7"},
		{"offer", &tg.MessageActionStarGiftPurchaseOffer{Gift: &tg.StarGiftUnique{Title: "Plush", Num: 7}, Price: &tg.StarsAmount{Amount: 300}}, "Offered 300 Stars for Plush #7"},
		{"offer declined expired", &tg.MessageActionStarGiftPurchaseOfferDeclined{Expired: true, Gift: &tg.StarGiftUnique{Title: "Plush", Num: 7}, Price: &tg.StarsTonAmount{Amount: 2_000_000_000}}, "The offer of 2 TON for Plush #7 expired"},
		{"post declined", &tg.MessageActionSuggestedPostApproval{Rejected: true, RejectComment: "Off topic"}, "Declined the suggested post: Off topic"},
		{"post approved", &tg.MessageActionSuggestedPostApproval{Price: &tg.StarsAmount{Amount: 50}, ScheduleDate: 1_700_000_000}, "Approved the suggested post for 50 Stars, scheduled for Nov 14, 2023 22:13 UTC"},
		{"post success", &tg.MessageActionSuggestedPostSuccess{Price: &tg.StarsAmount{Amount: 50}}, "The suggested post was published and paid 50 Stars"},
		{"post refund", &tg.MessageActionSuggestedPostRefund{PayerInitiated: true}, "The suggested post was refunded at the payer's request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := serviceMessageText(testServiceEnv(), tt.action)
			require.True(t, ok, "action has no text")
			assert.Equal(t, tt.want, got)
		})
	}
}

// Actions the bridge turns into room state must not also get a notice.
func TestServiceMessageTextSkipsRoomState(t *testing.T) {
	for _, action := range []tg.MessageActionClass{
		&tg.MessageActionChatEditTitle{Title: "x"},
		&tg.MessageActionChatAddUser{},
		&tg.MessageActionSetMessagesTTL{},
		&tg.MessageActionPhoneCall{},
		&tg.MessageActionTopicEdit{},
		&tg.MessageActionPinMessage{},
	} {
		_, ok := serviceMessageText(testServiceEnv(), action)
		assert.False(t, ok, "%T", action)
	}
}

// Every action type in the schema must be either worded or knowingly bridged as something else.
func TestEveryServiceActionIsCovered(t *testing.T) {
	seen := 0
	for id, ctor := range tg.TypesConstructorMap() {
		action, ok := ctor().(tg.MessageActionClass)
		if !ok {
			continue
		}
		seen++
		_, worded := serviceTexts[id]
		_, elsewhere := serviceActionsBridgedElsewhere[id]
		assert.True(t, worded != elsewhere, "%T must be in exactly one of the two tables (worded=%v elsewhere=%v)", action, worded, elsewhere)
	}
	assert.Greater(t, seen, 60)
}

// No wording may panic on an action with nothing filled in.
func TestServiceTextsHandleEmptyActions(t *testing.T) {
	for id := range serviceTexts {
		action := tg.TypesConstructorMap()[id]().(tg.MessageActionClass)
		assert.NotPanics(t, func() { serviceMessageText(testServiceEnv(), action) }, "%T", action)
	}
}
