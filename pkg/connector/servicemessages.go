package connector

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// serviceTextEnv is what the wording of a service message may need to look up.
type serviceTextEnv struct {
	// peerName is the display name of a user, chat or channel.
	peerName func(tg.PeerClass) string
}

func (env serviceTextEnv) userName(userID int64) string {
	return env.peerName(&tg.PeerUser{UserID: userID})
}

func (env serviceTextEnv) names(peers []tg.PeerClass) string {
	names := make([]string, 0, len(peers))
	for _, peer := range peers {
		names = append(names, env.peerName(peer))
	}
	return strings.Join(names, ", ")
}

type serviceTextFunc func(env serviceTextEnv, action tg.MessageActionClass) string

// serviceText adapts a typed wording function to the table.
func serviceText[T tg.MessageActionClass](f func(env serviceTextEnv, a T) string) serviceTextFunc {
	return func(env serviceTextEnv, action tg.MessageActionClass) string {
		return f(env, action.(T))
	}
}

// fixedText is a wording that carries no details.
func fixedText(text string) serviceTextFunc {
	return func(serviceTextEnv, tg.MessageActionClass) string { return text }
}

// serviceActionsBridgedElsewhere are the actions the bridge already turns into room state (or into its own
// message), so no extra notice is made for them.
var serviceActionsBridgedElsewhere = map[uint32]string{
	tg.MessageActionEmptyTypeID:              "nothing to show",
	tg.MessageActionChatEditTitleTypeID:      "room name",
	tg.MessageActionChatEditPhotoTypeID:      "room avatar",
	tg.MessageActionChatDeletePhotoTypeID:    "room avatar",
	tg.MessageActionChangeCommunityTypeID:    "room parent",
	tg.MessageActionChatAddUserTypeID:        "membership",
	tg.MessageActionChatJoinedByLinkTypeID:   "membership",
	tg.MessageActionChatDeleteUserTypeID:     "membership",
	tg.MessageActionChatCreateTypeID:         "room creation",
	tg.MessageActionChannelCreateTypeID:      "room creation",
	tg.MessageActionSetMessagesTTLTypeID:     "disappearing messages",
	tg.MessageActionPhoneCallTypeID:          "call message",
	tg.MessageActionGroupCallTypeID:          "video chat message",
	tg.MessageActionInviteToGroupCallTypeID:  "video chat message",
	tg.MessageActionGroupCallScheduledTypeID: "video chat message",
	tg.MessageActionChatMigrateToTypeID:      "portal migration",
	tg.MessageActionChannelMigrateFromTypeID: "portal migration",
	tg.MessageActionTopicCreateTypeID:        "topic rooms",
	tg.MessageActionTopicEditTypeID:          "topic rooms",
	tg.MessageActionPinMessageTypeID:         "pinned events",
}

// serviceTexts is the wording of every service action that isn't bridged as room state, worded the way
// Telegram's own apps word it, with the actor left to the message sender.
var serviceTexts = map[uint32]serviceTextFunc{
	tg.MessageActionScreenshotTakenTypeID:     fixedText("Took a screenshot"),
	tg.MessageActionContactSignUpTypeID:       fixedText("Joined Telegram"),
	tg.MessageActionChatJoinedByRequestTypeID: fixedText("Joined by request"),
	tg.MessageActionHistoryClearTypeID:        fixedText("Cleared the chat history"),

	tg.MessageActionCustomActionTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionCustomAction) string {
		return a.Message
	}),
	tg.MessageActionGameScoreTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionGameScore) string {
		return fmt.Sprintf("Scored %d in a game", a.Score)
	}),
	tg.MessageActionGeoProximityReachedTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionGeoProximityReached) string {
		return fmt.Sprintf("%s is now within %s of %s", env.peerName(a.FromID), formatDistance(a.Distance), env.peerName(a.ToID))
	}),
	tg.MessageActionBoostApplyTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionBoostApply) string {
		if a.Boosts <= 1 {
			return "Boosted this chat"
		}
		return fmt.Sprintf("Boosted this chat %d times", a.Boosts)
	}),
	tg.MessageActionBotAllowedTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionBotAllowed) string {
		switch {
		case a.Domain != "":
			return "Allowed the bot to message them by logging in on " + a.Domain
		case a.AttachMenu:
			return "Allowed the bot to message them from the attachment menu"
		case a.App != nil:
			return "Allowed the bot to message them by opening its app"
		default:
			return "Allowed the bot to message them"
		}
	}),
	tg.MessageActionChangeCreatorTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionChangeCreator) string {
		return "Transferred ownership to " + env.userName(a.NewCreatorID)
	}),
	tg.MessageActionNewCreatorPendingTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionNewCreatorPending) string {
		return "Started transferring ownership to " + env.userName(a.NewCreatorID)
	}),
	tg.MessageActionManagedBotCreatedTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionManagedBotCreated) string {
		return "Created the bot " + env.userName(a.BotID)
	}),
	tg.MessageActionConferenceCallTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionConferenceCall) string {
		kind := "conference call"
		if a.Video {
			kind = "video conference call"
		}
		var text string
		switch {
		case a.Missed:
			text = "Missed " + kind
		case a.Active:
			text = "Started a " + kind
		case a.Duration > 0:
			text = fmt.Sprintf("%s ended (%s)", capitalizeFirst(kind), formatCallDuration(a.Duration))
		default:
			text = capitalizeFirst(kind) + " ended"
		}
		if len(a.OtherParticipants) > 0 {
			text += " with " + env.names(a.OtherParticipants)
		}
		return text
	}),

	tg.MessageActionSetChatThemeTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionSetChatTheme) string {
		switch theme := a.Theme.(type) {
		case *tg.ChatTheme:
			if theme.Emoticon == "" {
				return "Turned off the chat theme"
			}
			return "Changed the chat theme to " + theme.Emoticon
		case *tg.ChatThemeUniqueGift:
			if name := giftName(theme.Gift); name != "" {
				return "Changed the chat theme to " + name
			}
		}
		return "Changed the chat theme"
	}),
	tg.MessageActionSetChatWallPaperTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionSetChatWallPaper) string {
		switch {
		case a.Same:
			return "Set the same wallpaper for this chat"
		case a.ForBoth:
			return "Set a new wallpaper for both of you"
		default:
			return "Changed the chat wallpaper"
		}
	}),

	tg.MessageActionNoForwardsToggleTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionNoForwardsToggle) string {
		if a.NewValue {
			return "Enabled content protection (forwarding and saving are restricted)"
		}
		return "Disabled content protection"
	}),
	tg.MessageActionNoForwardsRequestTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionNoForwardsRequest) string {
		want := "allow forwarding and saving"
		if a.NewValue {
			want = "restrict forwarding and saving"
		}
		if a.Expired {
			return "The request to " + want + " expired"
		}
		return "Asked to " + want
	}),

	tg.MessageActionPollAppendAnswerTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionPollAppendAnswer) string {
		return fmt.Sprintf("Added the option “%s” to the poll", pollAnswerText(a.Answer))
	}),
	tg.MessageActionPollDeleteAnswerTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionPollDeleteAnswer) string {
		return fmt.Sprintf("Removed the option “%s” from the poll", pollAnswerText(a.Answer))
	}),
	tg.MessageActionTodoAppendTasksTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionTodoAppendTasks) string {
		titles := make([]string, 0, len(a.List))
		for _, item := range a.List {
			titles = append(titles, "“"+item.Title.Text+"”")
		}
		return fmt.Sprintf("Added %s to the checklist: %s", plural(len(a.List), "task", "tasks"), strings.Join(titles, ", "))
	}),
	tg.MessageActionTodoCompletionsTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionTodoCompletions) string {
		var parts []string
		if len(a.Completed) > 0 {
			parts = append(parts, fmt.Sprintf("Marked %s as done", plural(len(a.Completed), "checklist item", "checklist items")))
		}
		if len(a.Incompleted) > 0 {
			parts = append(parts, fmt.Sprintf("Marked %s as not done", plural(len(a.Incompleted), "checklist item", "checklist items")))
		}
		if len(parts) == 0 {
			return "Updated the checklist"
		}
		return strings.Join(parts, ", ")
	}),

	tg.MessageActionSuggestProfilePhotoTypeID: fixedText("Suggested a profile photo"),
	tg.MessageActionSuggestBirthdayTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionSuggestBirthday) string {
		b := a.Birthday
		text := fmt.Sprintf("%d %s", b.Day, time.Month(b.Month).String())
		if b.Year != 0 {
			text += " " + strconv.Itoa(b.Year)
		}
		return "Suggested a birthday: " + text
	}),
	tg.MessageActionRequestedPeerTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionRequestedPeer) string {
		return "Shared " + env.names(a.Peers)
	}),
	tg.MessageActionRequestedPeerSentMeTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionRequestedPeerSentMe) string {
		names := make([]string, 0, len(a.Peers))
		for _, peer := range a.Peers {
			switch p := peer.(type) {
			case *tg.RequestedPeerUser:
				name := strings.TrimSpace(p.FirstName + " " + p.LastName)
				if name == "" {
					name = p.Username
				}
				names = append(names, name)
			case *tg.RequestedPeerChat:
				names = append(names, p.Title)
			case *tg.RequestedPeerChannel:
				names = append(names, p.Title)
			}
		}
		return "Shared " + strings.Join(names, ", ")
	}),
	tg.MessageActionSecureValuesSentTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionSecureValuesSent) string {
		names := make([]string, 0, len(a.Types))
		for _, t := range a.Types {
			names = append(names, secureValueName(t))
		}
		return "Shared Telegram Passport data: " + strings.Join(names, ", ")
	}),
	tg.MessageActionSecureValuesSentMeTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionSecureValuesSentMe) string {
		return fmt.Sprintf("Shared %s of Telegram Passport data", plural(len(a.Values), "item", "items"))
	}),
	tg.MessageActionWebViewDataSentTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionWebViewDataSent) string {
		return fmt.Sprintf("Sent data from the mini app button “%s”", a.Text)
	}),
	tg.MessageActionWebViewDataSentMeTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionWebViewDataSentMe) string {
		return fmt.Sprintf("Sent data from the mini app button “%s”", a.Text)
	}),

	tg.MessageActionPaymentSentTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionPaymentSent) string {
		return paymentText("Paid", a.Currency, a.TotalAmount, a.RecurringInit, a.RecurringUsed, a.SubscriptionUntilDate)
	}),
	tg.MessageActionPaymentSentMeTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionPaymentSentMe) string {
		return paymentText("Received a payment of", a.Currency, a.TotalAmount, a.RecurringInit, a.RecurringUsed, a.SubscriptionUntilDate)
	}),
	tg.MessageActionPaymentRefundedTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionPaymentRefunded) string {
		return fmt.Sprintf("Refunded %s to %s", formatMoney(a.Currency, a.TotalAmount), env.peerName(a.Peer))
	}),
	tg.MessageActionPaidMessagesPriceTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionPaidMessagesPrice) string {
		if a.Stars == 0 {
			return "Made messages free"
		}
		return "Set the price of a message to " + formatStars(a.Stars)
	}),
	tg.MessageActionPaidMessagesRefundedTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionPaidMessagesRefunded) string {
		return fmt.Sprintf("Refunded %s (%s)", plural(a.Count, "paid message", "paid messages"), formatStars(a.Stars))
	}),

	tg.MessageActionGiftPremiumTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionGiftPremium) string {
		text := "Gifted Telegram Premium for " + formatMonths(a.Days)
		if price := giftPrice(a.Currency, a.Amount, a.CryptoCurrency, a.CryptoAmount); price != "" {
			text += " (" + price + ")"
		}
		return withGiftMessage(text, a.Message.Text)
	}),
	tg.MessageActionGiftCodeTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionGiftCode) string {
		var text string
		switch {
		case a.ViaGiveaway && a.Unclaimed:
			text = "Unclaimed giveaway prize: a Telegram Premium gift code for " + formatMonths(a.Days)
		case a.ViaGiveaway:
			text = "Won a Telegram Premium gift code for " + formatMonths(a.Days) + " in a giveaway"
		default:
			text = "Gave a Telegram Premium gift code for " + formatMonths(a.Days)
		}
		if a.BoostPeer != nil {
			text += " from " + env.peerName(a.BoostPeer)
		}
		if price := giftPrice(a.Currency, a.Amount, a.CryptoCurrency, a.CryptoAmount); price != "" {
			text += " (" + price + ")"
		}
		return withGiftMessage(text, a.Message.Text)
	}),
	tg.MessageActionGiftStarsTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionGiftStars) string {
		text := "Gifted " + formatStars(a.Stars)
		if price := giftPrice(a.Currency, a.Amount, a.CryptoCurrency, a.CryptoAmount); price != "" {
			text += " (" + price + ")"
		}
		return text
	}),
	tg.MessageActionGiftTonTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionGiftTon) string {
		text := "Gifted " + formatCrypto(a.CryptoCurrency, a.CryptoAmount)
		if a.Currency != "" {
			text += " (" + formatMoney(a.Currency, a.Amount) + ")"
		}
		return text
	}),
	tg.MessageActionPrizeStarsTypeID: serviceText(func(env serviceTextEnv, a *tg.MessageActionPrizeStars) string {
		text := "Won " + formatStars(a.Stars) + " in a giveaway"
		if a.Unclaimed {
			text = "Unclaimed giveaway prize: " + formatStars(a.Stars)
		}
		if a.BoostPeer != nil {
			text += " from " + env.peerName(a.BoostPeer)
		}
		return text
	}),
	tg.MessageActionGiveawayLaunchTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionGiveawayLaunch) string {
		if a.Stars > 0 {
			return "Started a giveaway of " + formatStars(a.Stars)
		}
		return "Started a giveaway"
	}),
	tg.MessageActionGiveawayResultsTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionGiveawayResults) string {
		kind := "Giveaway"
		if a.Stars {
			kind = "Stars giveaway"
		}
		if a.WinnersCount == 0 {
			return kind + " ended with no winners"
		}
		text := fmt.Sprintf("%s ended with %s", kind, plural(a.WinnersCount, "winner", "winners"))
		if a.UnclaimedCount > 0 {
			text += fmt.Sprintf(" (%d unclaimed)", a.UnclaimedCount)
		}
		return text
	}),
	tg.MessageActionStarGiftTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionStarGift) string {
		name := giftName(a.Gift)
		if name == "" {
			name = "a gift"
		}
		var text string
		switch {
		case a.Refunded:
			text = "Gift refunded: " + name
		case a.Upgraded:
			text = "Upgraded the gift " + name
		case a.Converted:
			text = fmt.Sprintf("Converted the gift %s to %s", name, formatStars(a.ConvertStars))
		default:
			text = "Sent a gift: " + name
			if gift, ok := a.Gift.(*tg.StarGift); ok && gift.Stars > 0 {
				text += " (" + formatStars(gift.Stars) + ")"
			}
		}
		return withGiftMessage(text, a.Message.Text)
	}),
	tg.MessageActionStarGiftUniqueTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionStarGiftUnique) string {
		name := giftName(a.Gift)
		if name == "" {
			name = "a collectible gift"
		}
		var text string
		switch {
		case a.Refunded:
			text = "Collectible gift refunded: " + name
		case a.Upgrade:
			text = "Upgraded a gift to a collectible: " + name
		case a.Transferred:
			text = "Transferred a collectible gift: " + name
		default:
			text = "Sent a collectible gift: " + name
		}
		if a.ResaleAmount != nil {
			text += " for " + formatStarsAmount(a.ResaleAmount)
		}
		return text
	}),
	tg.MessageActionStarGiftPurchaseOfferTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionStarGiftPurchaseOffer) string {
		gift := giftName(a.Gift)
		if gift == "" {
			gift = "a gift"
		}
		price := formatStarsAmount(a.Price)
		switch {
		case a.Accepted:
			return fmt.Sprintf("Accepted the offer of %s for %s", price, gift)
		case a.Declined:
			return fmt.Sprintf("Declined the offer of %s for %s", price, gift)
		default:
			return fmt.Sprintf("Offered %s for %s", price, gift)
		}
	}),
	tg.MessageActionStarGiftPurchaseOfferDeclinedTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionStarGiftPurchaseOfferDeclined) string {
		gift := giftName(a.Gift)
		if gift == "" {
			gift = "a gift"
		}
		price := formatStarsAmount(a.Price)
		if a.Expired {
			return fmt.Sprintf("The offer of %s for %s expired", price, gift)
		}
		return fmt.Sprintf("Declined the offer of %s for %s", price, gift)
	}),
	tg.MessageActionSuggestedPostApprovalTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionSuggestedPostApproval) string {
		switch {
		case a.Rejected:
			text := "Declined the suggested post"
			if a.RejectComment != "" {
				text += ": " + a.RejectComment
			}
			return text
		case a.BalanceTooLow:
			return "Could not approve the suggested post: not enough Stars"
		}
		text := "Approved the suggested post"
		if a.Price != nil {
			text += " for " + formatStarsAmount(a.Price)
		}
		if a.ScheduleDate != 0 {
			text += ", scheduled for " + time.Unix(int64(a.ScheduleDate), 0).UTC().Format("Jan 2, 2006 15:04 UTC")
		}
		return text
	}),
	tg.MessageActionSuggestedPostSuccessTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionSuggestedPostSuccess) string {
		text := "The suggested post was published"
		if a.Price != nil {
			text += " and paid " + formatStarsAmount(a.Price)
		}
		return text
	}),
	tg.MessageActionSuggestedPostRefundTypeID: serviceText(func(_ serviceTextEnv, a *tg.MessageActionSuggestedPostRefund) string {
		if a.PayerInitiated {
			return "The suggested post was refunded at the payer's request"
		}
		return "The suggested post was refunded because it was removed early"
	}),
}

// serviceMessageText is the notice text for a service action. ok is false for actions that the bridge turns
// into room state or into its own message instead, and for actions this doesn't know.
func serviceMessageText(env serviceTextEnv, action tg.MessageActionClass) (text string, ok bool) {
	f, found := serviceTexts[action.TypeID()]
	if !found {
		return "", false
	}
	return f(env, action), true
}

func (tc *TelegramClient) serviceTextEnv(ctx context.Context) serviceTextEnv {
	return serviceTextEnv{peerName: func(peer tg.PeerClass) string {
		switch p := peer.(type) {
		case *tg.PeerUser:
			ghost, err := tc.main.Bridge.GetGhostByID(ctx, ids.MakeUserID(p.UserID))
			if err == nil && ghost != nil && ghost.Name != "" {
				return ghost.Name
			}
			return "user " + strconv.FormatInt(p.UserID, 10)
		case *tg.PeerChat:
			return tc.portalName(ctx, ids.PeerTypeChat, p.ChatID)
		case *tg.PeerChannel:
			return tc.portalName(ctx, ids.PeerTypeChannel, p.ChannelID)
		}
		return "someone"
	}}
}

func (tc *TelegramClient) portalName(ctx context.Context, peerType ids.PeerType, id int64) string {
	portal, err := tc.main.Bridge.GetExistingPortalByKey(ctx, tc.makePortalKeyFromID(peerType, id, 0))
	if err == nil && portal != nil && portal.Name != "" {
		return portal.Name
	}
	return "a chat"
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func formatStars(n int64) string {
	if n == 1 {
		return "1 Star"
	}
	return fmt.Sprintf("%d Stars", n)
}

func formatStarsAmount(amount tg.StarsAmountClass) string {
	switch a := amount.(type) {
	case *tg.StarsAmount:
		return formatStars(a.Amount)
	case *tg.StarsTonAmount:
		return formatCrypto("TON", a.Amount)
	}
	return "an unknown price"
}

// formatMonths words a Premium duration given in days.
func formatMonths(days int) string {
	if days > 0 && days%365 == 0 {
		return plural(days/365, "year", "years")
	}
	if days > 0 && days%30 == 0 {
		return plural(days/30, "month", "months")
	}
	return plural(days, "day", "days")
}

// zeroDecimalCurrencies are the currencies whose amounts Telegram sends without minor units.
var zeroDecimalCurrencies = map[string]bool{"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "UGX": true, "XAF": true, "XOF": true, "PYG": true}

func formatMoney(currency string, amount int64) string {
	switch {
	case currency == "XTR":
		return formatStars(amount)
	case zeroDecimalCurrencies[currency]:
		return fmt.Sprintf("%d %s", amount, currency)
	default:
		return fmt.Sprintf("%d.%02d %s", amount/100, amount%100, currency)
	}
}

// formatCrypto words an amount of a crypto currency, given in nano units (TON).
func formatCrypto(currency string, amount int64) string {
	if currency == "" {
		currency = "TON"
	}
	whole, frac := amount/1_000_000_000, amount%1_000_000_000
	text := strconv.FormatInt(whole, 10)
	if frac != 0 {
		text += "." + strings.TrimRight(fmt.Sprintf("%09d", frac), "0")
	}
	return text + " " + currency
}

func giftPrice(currency string, amount int64, cryptoCurrency string, cryptoAmount int64) string {
	switch {
	case currency != "":
		return formatMoney(currency, amount)
	case cryptoCurrency != "":
		return formatCrypto(cryptoCurrency, cryptoAmount)
	}
	return ""
}

func withGiftMessage(text, message string) string {
	if message == "" {
		return text
	}
	return text + "\n“" + message + "”"
}

func giftName(gift tg.StarGiftClass) string {
	switch g := gift.(type) {
	case *tg.StarGift:
		return g.Title
	case *tg.StarGiftUnique:
		if g.Num > 0 {
			return fmt.Sprintf("%s #%d", g.Title, g.Num)
		}
		return g.Title
	}
	return ""
}

func pollAnswerText(answer tg.PollAnswerClass) string {
	if a, ok := answer.(*tg.PollAnswer); ok {
		return a.Text.Text
	}
	return ""
}

func paymentText(prefix, currency string, amount int64, recurringInit, recurringUsed bool, until int) string {
	text := prefix + " " + formatMoney(currency, amount)
	switch {
	case recurringInit:
		text += " (started a subscription)"
	case recurringUsed:
		text += " (subscription renewed)"
	}
	if until != 0 {
		text += ", active until " + time.Unix(int64(until), 0).UTC().Format("Jan 2, 2006")
	}
	return text
}

func formatDistance(meters int) string {
	if meters < 1000 {
		return fmt.Sprintf("%d m", meters)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(meters)/1000), ".0") + " km"
}

func formatCallDuration(seconds int) string {
	d := time.Duration(seconds) * time.Second
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func secureValueName(t tg.SecureValueTypeClass) string {
	switch t.(type) {
	case *tg.SecureValueTypePersonalDetails:
		return "personal details"
	case *tg.SecureValueTypePassport:
		return "passport"
	case *tg.SecureValueTypeDriverLicense:
		return "driver's license"
	case *tg.SecureValueTypeIdentityCard:
		return "identity card"
	case *tg.SecureValueTypeInternalPassport:
		return "internal passport"
	case *tg.SecureValueTypeAddress:
		return "address"
	case *tg.SecureValueTypeUtilityBill:
		return "utility bill"
	case *tg.SecureValueTypeBankStatement:
		return "bank statement"
	case *tg.SecureValueTypeRentalAgreement:
		return "rental agreement"
	case *tg.SecureValueTypePassportRegistration:
		return "passport registration"
	case *tg.SecureValueTypeTemporaryRegistration:
		return "temporary registration"
	case *tg.SecureValueTypePhone:
		return "phone number"
	case *tg.SecureValueTypeEmail:
		return "email address"
	}
	return "document"
}
