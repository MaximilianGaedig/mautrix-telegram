package connector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func testChecklist(completed ...int) *tg.MessageMediaToDo {
	todo := &tg.MessageMediaToDo{Todo: tg.TodoList{
		Title: tg.TextWithEntities{Text: "Shopping <list>"},
		List: []tg.TodoItem{
			{ID: 1, Title: tg.TextWithEntities{Text: "Milk"}},
			{ID: 2, Title: tg.TextWithEntities{Text: "Eggs & bacon"}},
		},
	}}
	for _, id := range completed {
		todo.Completions = append(todo.Completions, tg.TodoCompletion{ID: id, CompletedBy: &tg.PeerUser{UserID: 1}})
	}
	return todo
}

func TestSummariseMediaText(t *testing.T) {
	// 2026-10-02 12:00 UTC
	const until = 1790942400
	tests := []struct {
		name  string
		media tg.MessageMediaClass
		want  string
	}{
		{"checklist", testChecklist(2), "Shopping <list>\n☐ Milk\n☑ Eggs & bacon"},
		{"checklist untitled", &tg.MessageMediaToDo{Todo: tg.TodoList{List: []tg.TodoItem{{ID: 7, Title: tg.TextWithEntities{Text: "Call"}}}}}, "Checklist\n☐ Call"},
		{
			"giveaway premium",
			&tg.MessageMediaGiveaway{Quantity: 5, Months: 3, Channels: []int64{10}, UntilDate: until},
			"Giveaway: Telegram Premium for 3 months for 5 winners\nOpen to subscribers of Test Channel\nWinners are picked on Oct 2, 2026",
		},
		{
			"giveaway stars",
			&tg.MessageMediaGiveaway{Quantity: 1, Stars: 500, OnlyNewSubscribers: true, Channels: []int64{10, 11}, CountriesISO2: []string{"DE", "PL"}, PrizeDescription: "signed posters"},
			"Giveaway: 500 Stars shared among 1 winner\nAlso: 1 signed posters\nOpen to new subscribers of Test Channel, Test Channel from DE, PL",
		},
		{
			"giveaway results",
			&tg.MessageMediaGiveawayResults{WinnersCount: 5, Winners: []int64{1, 2}, Months: 6, UnclaimedCount: 1},
			"Giveaway results: 5 winners: Alice, Bob and 3 more\nPrize: Telegram Premium for 6 months\n1 prize was not claimed",
		},
		{
			"giveaway results stars",
			&tg.MessageMediaGiveawayResults{WinnersCount: 1, Winners: []int64{2}, Stars: 100},
			"Giveaway results: 1 winner: Bob\nPrize: 100 Stars shared among the winners",
		},
		{"giveaway refunded", &tg.MessageMediaGiveawayResults{Refunded: true, WinnersCount: 3}, "The giveaway was cancelled and the prizes were refunded"},
		{
			"invoice",
			&tg.MessageMediaInvoice{Title: "Coffee", Description: "A large one", Currency: "EUR", TotalAmount: 450},
			"Invoice: Coffee, 4.50 EUR\nA large one",
		},
		{"invoice paid in stars", &tg.MessageMediaInvoice{Title: "Sticker", Currency: "XTR", TotalAmount: 25, ReceiptMsgID: 9, Test: true}, "Paid invoice (test): Sticker, 25 Stars"},
		{
			"paid media locked",
			&tg.MessageMediaPaidMedia{StarsAmount: 50, ExtendedMedia: []tg.MessageExtendedMediaClass{
				&tg.MessageExtendedMediaPreview{}, &tg.MessageExtendedMediaPreview{}, &tg.MessageExtendedMediaPreview{VideoDuration: 12},
			}},
			"Paid media: 2 photos and 1 video for 50 Stars. Open Telegram to view.",
		},
		{
			"paid media bought",
			&tg.MessageMediaPaidMedia{StarsAmount: 1, ExtendedMedia: []tg.MessageExtendedMediaClass{
				&tg.MessageExtendedMedia{Media: &tg.MessageMediaDocument{}},
			}},
			"Paid media: 1 video for 1 Star. Open Telegram to view.",
		},
		{"story", &tg.MessageMediaStory{Peer: &tg.PeerUser{UserID: 1}, ID: 4}, "Story from Alice. Open Telegram to view."},
		{
			"story with caption",
			&tg.MessageMediaStory{Peer: &tg.PeerChannel{ChannelID: 10}, ID: 4, Story: &tg.StoryItem{Caption: "At the beach"}},
			"Story from Test Channel: At the beach. Open Telegram to view.",
		},
		{"story mention", &tg.MessageMediaStory{Peer: &tg.PeerUser{UserID: 2}, ID: 4, ViaMention: true}, "Bob mentioned you in a story. Open Telegram to view."},
		{"story gone", &tg.MessageMediaStory{Peer: &tg.PeerUser{UserID: 2}, ID: 4, Story: &tg.StoryItemDeleted{ID: 4}}, "Story from Bob (no longer available)"},
		{"live stream", &tg.MessageMediaVideoStream{}, "Live stream. Open Telegram to watch."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, ok := summariseMedia(testServiceEnv(), tt.media)
			require.True(t, ok)
			assert.Equal(t, tt.want, summary.Body)
		})
	}

	_, ok := summariseMedia(testServiceEnv(), &tg.MessageMediaUnsupported{})
	assert.False(t, ok, "media the bridge doesn't know must fall through to the generic notice")
}

func TestChecklistPart(t *testing.T) {
	media := testChecklist(2)
	summary, ok := summariseMedia(testServiceEnv(), media)
	require.True(t, ok)
	part, _ := summary.part(media)

	assert.Equal(t, event.MsgText, part.Content.MsgType)
	assert.Equal(t, event.FormatHTML, part.Content.Format)
	assert.Equal(t, "<strong>Shopping &lt;list&gt;</strong><br>☐ Milk<br>☑ Eggs &amp; bacon", part.Content.FormattedBody)
	assert.NotContains(t, part.Extra, "fi.mau.telegram.unsupported")
	assert.Equal(t, map[string]any{
		"title": "Shopping <list>",
		"items": []map[string]any{
			{"id": 1, "title": "Milk", "done": false},
			{"id": 2, "title": "Eggs & bacon", "done": true},
		},
	}, part.Extra["fi.mau.telegram.todo"])
}

// A message with one of these media used to come out as "messageMediaToDo are not yet supported".
func TestMediaToMatrixSummarisesMedia(t *testing.T) {
	tc := &TelegramClient{}
	convert := func(media tg.MessageMediaClass) (*event.MessageEventContent, map[string]any, []byte) {
		msg := &tg.Message{ID: 1}
		// The setter also sets the flag that says the message has media at all.
		msg.SetMedia(media)
		part, disappear, hashInput := tc.mediaToMatrix(context.Background(), nil, nil, msg)
		require.NotNil(t, part)
		assert.Nil(t, disappear)
		return part.Content, part.Extra, hashInput
	}

	content, extra, unticked := convert(testChecklist())
	assert.Equal(t, event.MsgText, content.MsgType)
	assert.Equal(t, "Shopping <list>\n☐ Milk\n☐ Eggs & bacon", content.Body)
	assert.NotContains(t, extra, "fi.mau.telegram.unsupported")

	// Ticking an item edits the message without touching its text, so the tick has to change the hash input
	// or the edit is dropped as a no-op.
	_, _, ticked := convert(testChecklist(1))
	assert.NotEqual(t, unticked, ticked)

	content, extra, _ = convert(&tg.MessageMediaPaidMedia{StarsAmount: 5, ExtendedMedia: []tg.MessageExtendedMediaClass{&tg.MessageExtendedMediaPreview{}}})
	assert.Equal(t, event.MsgNotice, content.MsgType)
	assert.Equal(t, "Paid media: 1 photo for 5 Stars. Open Telegram to view.", content.Body)
	assert.Equal(t, true, extra["fi.mau.telegram.unsupported"])
	assert.Equal(t, uint32(tg.MessageMediaPaidMediaTypeID), extra["fi.mau.telegram.type_id"])
}
