package connector

import (
	"fmt"
	"html"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// mediaSummary is what a message shows in place of a kind of Telegram media that Matrix has nothing for.
type mediaSummary struct {
	Body string
	// HTML is only set when the summary has structure that plain text loses.
	HTML string
	// Pointer is true when the summary only says what is there and the content itself has to be opened in
	// Telegram. Those stay notices and keep the unsupported flag, like the generic fallback they replace.
	Pointer bool
	// Extra goes into the event content next to the body.
	Extra map[string]any
}

const openTelegramToView = "Open Telegram to view."

// summariseMedia words the media kinds that are bridged as text: checklists, giveaways and their results,
// invoices, paid media, stories and live streams. ok is false for every other kind.
func summariseMedia(env serviceTextEnv, media tg.MessageMediaClass) (summary mediaSummary, ok bool) {
	switch m := media.(type) {
	case *tg.MessageMediaToDo:
		return summariseToDo(m), true
	case *tg.MessageMediaGiveaway:
		return mediaSummary{Body: giveawayText(env, m)}, true
	case *tg.MessageMediaGiveawayResults:
		return mediaSummary{Body: giveawayResultsText(env, m)}, true
	case *tg.MessageMediaInvoice:
		return mediaSummary{Body: invoiceText(m)}, true
	case *tg.MessageMediaPaidMedia:
		return mediaSummary{Body: paidMediaText(m), Pointer: true}, true
	case *tg.MessageMediaStory:
		return mediaSummary{Body: storyText(env, m), Pointer: true}, true
	case *tg.MessageMediaVideoStream:
		return mediaSummary{Body: "Live stream. Open Telegram to watch.", Pointer: true}, true
	}
	return mediaSummary{}, false
}

// part is the message part for the summary. The second return value goes into the message's content hash, so
// that an edit which only changes the media (a checklist item being ticked) is not taken for a no-op.
func (s mediaSummary) part(media tg.MessageMediaClass) (*bridgev2.ConvertedMessagePart, []byte) {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: s.Body}
	if s.HTML != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = s.HTML
	}
	extra := map[string]any{"fi.mau.telegram.type_id": media.TypeID()}
	for key, value := range s.Extra {
		extra[key] = value
	}
	if s.Pointer {
		content.MsgType = event.MsgNotice
		extra["fi.mau.telegram.unsupported"] = true
	}
	return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: content, Extra: extra}, []byte(s.Body)
}

func summariseToDo(m *tg.MessageMediaToDo) mediaSummary {
	done := make(map[int]bool, len(m.Completions))
	for _, completion := range m.Completions {
		done[completion.ID] = true
	}
	title := m.Todo.Title.Text
	if title == "" {
		title = "Checklist"
	}
	var body, formatted strings.Builder
	body.WriteString(title)
	formatted.WriteString("<strong>" + html.EscapeString(title) + "</strong>")
	items := make([]map[string]any, 0, len(m.Todo.List))
	for _, item := range m.Todo.List {
		box := "☐"
		if done[item.ID] {
			box = "☑"
		}
		body.WriteString("\n" + box + " " + item.Title.Text)
		formatted.WriteString("<br>" + box + " " + html.EscapeString(item.Title.Text))
		items = append(items, map[string]any{"id": item.ID, "title": item.Title.Text, "done": done[item.ID]})
	}
	return mediaSummary{
		Body: body.String(),
		HTML: formatted.String(),
		Extra: map[string]any{
			"fi.mau.telegram.todo": map[string]any{"title": m.Todo.Title.Text, "items": items},
		},
	}
}

func formatDay(unix int) string {
	return time.Unix(int64(unix), 0).UTC().Format("Jan 2, 2006")
}

// premiumMonths words the length of a Premium prize. Telegram sends zero months for a Stars giveaway.
func premiumMonths(months int) string {
	return plural(months, "month", "months")
}

func giveawayText(env serviceTextEnv, m *tg.MessageMediaGiveaway) string {
	var text strings.Builder
	winners := plural(m.Quantity, "winner", "winners")
	if m.Stars > 0 {
		fmt.Fprintf(&text, "Giveaway: %s shared among %s", formatStars(m.Stars), winners)
	} else {
		fmt.Fprintf(&text, "Giveaway: Telegram Premium for %s for %s", premiumMonths(m.Months), winners)
	}
	if m.PrizeDescription != "" {
		// Telegram shows the quantity in front of the description, which is written to follow a number.
		fmt.Fprintf(&text, "\nAlso: %d %s", m.Quantity, m.PrizeDescription)
	}
	if len(m.Channels) > 0 {
		channels := make([]tg.PeerClass, 0, len(m.Channels))
		for _, channelID := range m.Channels {
			channels = append(channels, &tg.PeerChannel{ChannelID: channelID})
		}
		who := "subscribers"
		if m.OnlyNewSubscribers {
			who = "new subscribers"
		}
		fmt.Fprintf(&text, "\nOpen to %s of %s", who, env.names(channels))
		if len(m.CountriesISO2) > 0 {
			text.WriteString(" from " + strings.Join(m.CountriesISO2, ", "))
		}
	}
	if m.UntilDate != 0 {
		text.WriteString("\nWinners are picked on " + formatDay(m.UntilDate))
	}
	return text.String()
}

func giveawayResultsText(env serviceTextEnv, m *tg.MessageMediaGiveawayResults) string {
	if m.Refunded {
		return "The giveaway was cancelled and the prizes were refunded"
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Giveaway results: %s", plural(m.WinnersCount, "winner", "winners"))
	if len(m.Winners) > 0 {
		winners := make([]tg.PeerClass, 0, len(m.Winners))
		for _, userID := range m.Winners {
			winners = append(winners, &tg.PeerUser{UserID: userID})
		}
		text.WriteString(": " + env.names(winners))
		// Only the first winners are listed in the message, the rest are behind a button in Telegram.
		if more := m.WinnersCount - len(m.Winners); more > 0 {
			fmt.Fprintf(&text, " and %d more", more)
		}
	}
	if m.Stars > 0 {
		text.WriteString("\nPrize: " + formatStars(m.Stars) + " shared among the winners")
	} else if m.Months > 0 {
		text.WriteString("\nPrize: Telegram Premium for " + premiumMonths(m.Months))
	}
	if m.PrizeDescription != "" {
		text.WriteString("\nAlso: " + m.PrizeDescription)
	}
	if m.UnclaimedCount > 0 {
		text.WriteString("\n" + plural(m.UnclaimedCount, "prize was", "prizes were") + " not claimed")
	}
	return text.String()
}

func invoiceText(m *tg.MessageMediaInvoice) string {
	kind := "Invoice"
	if m.ReceiptMsgID != 0 {
		// An invoice that points at its receipt has been paid.
		kind = "Paid invoice"
	}
	if m.Test {
		kind += " (test)"
	}
	text := fmt.Sprintf("%s: %s, %s", kind, m.Title, formatMoney(m.Currency, m.TotalAmount))
	if m.Description != "" {
		text += "\n" + m.Description
	}
	return text
}

func paidMediaText(m *tg.MessageMediaPaidMedia) string {
	var videos int
	for _, item := range m.ExtendedMedia {
		switch item := item.(type) {
		case *tg.MessageExtendedMediaPreview:
			// A locked item is a blurred preview, and only a video's preview has a duration.
			if item.VideoDuration > 0 {
				videos++
			}
		case *tg.MessageExtendedMedia:
			if _, isDocument := item.Media.(*tg.MessageMediaDocument); isDocument {
				videos++
			}
		}
	}
	var what string
	switch photos := len(m.ExtendedMedia) - videos; {
	case photos > 0 && videos > 0:
		what = plural(photos, "photo", "photos") + " and " + plural(videos, "video", "videos")
	case videos > 0:
		what = plural(videos, "video", "videos")
	default:
		what = plural(photos, "photo", "photos")
	}
	return fmt.Sprintf("Paid media: %s for %s. %s", what, formatStars(m.StarsAmount), openTelegramToView)
}

func storyText(env serviceTextEnv, m *tg.MessageMediaStory) string {
	text := "Story from " + env.peerName(m.Peer)
	if m.ViaMention {
		text = env.peerName(m.Peer) + " mentioned you in a story"
	}
	switch story := m.Story.(type) {
	case *tg.StoryItem:
		if story.Caption != "" {
			text += ": " + story.Caption
		}
	case *tg.StoryItemDeleted:
		// Telegram sends this in place of a story that expired or was removed, so there is nothing to open.
		return text + " (no longer available)"
	}
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return text + " " + openTelegramToView
}
