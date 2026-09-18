// mautrix-telegram - A Matrix-Telegram puppeting bridge.
// Copyright (C) 2026 Maximilian Gaedig
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// fakeButtonResolver is a buttonResolver for tests that doesn't need a real TelegramClient/database.
type fakeButtonResolver struct {
	botUsername string
	userMXID    string
}

func (f *fakeButtonResolver) resolveBotUsername(context.Context, *tg.Message) string {
	return f.botUsername
}
func (f *fakeButtonResolver) resolveUserMXID(context.Context, int64) string { return f.userMXID }

func TestConvertReplyMarkup_NoMarkup(t *testing.T) {
	msg := &tg.Message{ID: 5}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	assert.Nil(t, kb)
}

func TestConvertReplyMarkup_EmptyInlineMarkup(t *testing.T) {
	msg := &tg.Message{ID: 5, ReplyMarkup: &tg.ReplyInlineMarkup{}}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	assert.Nil(t, kb)
}

func TestConvertReplyMarkup_RowWithNoButtons(t *testing.T) {
	msg := &tg.Message{ID: 5, ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{}}}}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	assert.Nil(t, kb)
}

func TestConvertReplyMarkup_InlineCallbackAndURL(t *testing.T) {
	msg := &tg.Message{
		ID: 12345,
		ReplyMarkup: &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardButtonRow{
				{Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonCallback{Text: "Reject Group Transfer", Data: []byte("reject")},
					&tg.KeyboardButtonURL{Text: "Docs", URL: "https://example.com"},
				}},
				{Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonGame{Text: "Whatever unhandled in test"},
				}},
			},
		},
	}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	require.NotNil(t, kb)
	assert.Equal(t, keyboardKindInline, kb.Keyboard)
	assert.Equal(t, 12345, kb.MessageID)
	require.Len(t, kb.Rows, 2)
	require.Len(t, kb.Rows[0], 2)

	assert.Equal(t, inlineButton{
		Text:    "Reject Group Transfer",
		Type:    buttonTypeCallback,
		Command: "!tg click 12345 0 0",
	}, kb.Rows[0][0])
	assert.Equal(t, inlineButton{
		Text: "Docs",
		Type: buttonTypeURL,
		URL:  "https://example.com",
	}, kb.Rows[0][1])

	require.Len(t, kb.Rows[1], 1)
	assert.Equal(t, buttonTypeGame, kb.Rows[1][0].Type)
	assert.Equal(t, "!tg click 12345 1 0", kb.Rows[1][0].Command)

	// The example from the design doc should marshal to the documented shape.
	data, err := json.Marshal(kb)
	require.NoError(t, err)
	var roundTrip map[string]any
	require.NoError(t, json.Unmarshal(data, &roundTrip))
	assert.Equal(t, "inline", roundTrip["keyboard"])
	assert.EqualValues(t, 12345, roundTrip["message_id"])
}

func TestConvertReplyMarkup_UnsupportedButtonsKeepIndexesValid(t *testing.T) {
	msg := &tg.Message{
		ID: 1,
		ReplyMarkup: &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardButtonRow{
				{Buttons: []tg.KeyboardButtonClass{
					&tg.KeyboardButtonBuy{Text: "Buy"},
					&tg.KeyboardButtonCallback{Text: "Press me", Data: []byte("x")},
				}},
			},
		},
	}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	require.NotNil(t, kb)
	require.Len(t, kb.Rows[0], 2)
	assert.Equal(t, buttonTypeUnsupported, kb.Rows[0][0].Type)
	assert.Equal(t, "Buy", kb.Rows[0][0].Text)
	assert.Empty(t, kb.Rows[0][0].Command)
	// The second button's command must still reference column 1, not 0.
	assert.Equal(t, "!tg click 1 0 1", kb.Rows[0][1].Command)
}

func TestConvertReplyMarkup_CallbackRequiresPassword(t *testing.T) {
	msg := &tg.Message{
		ID: 1,
		ReplyMarkup: &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonCallback{Text: "2FA", Data: []byte("x"), RequiresPassword: true},
			}}},
		},
	}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	require.NotNil(t, kb)
	btn := kb.Rows[0][0]
	assert.Equal(t, buttonTypeCallback, btn.Type)
	assert.True(t, btn.RequiresPassword)
	assert.NotEmpty(t, btn.Command)
}

func TestConvertReplyMarkup_UnusableURLIsUnsupported(t *testing.T) {
	msg := &tg.Message{
		ID: 1,
		ReplyMarkup: &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonURL{Text: "Evil", URL: "javascript:alert(1)"},
			}}},
		},
	}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	require.NotNil(t, kb)
	assert.Equal(t, buttonTypeUnsupported, kb.Rows[0][0].Type)
	assert.Empty(t, kb.Rows[0][0].URL)
}

func TestConvertReplyMarkup_CommandUsesConfiguredPrefix(t *testing.T) {
	msg := &tg.Message{
		ID: 42,
		ReplyMarkup: &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonCallback{Text: "Go", Data: []byte("x")},
			}}},
		},
	}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!custom-prefix")
	require.NotNil(t, kb)
	assert.Equal(t, "!custom-prefix click 42 0 0", kb.Rows[0][0].Command)
}

func TestConvertReplyMarkup_PlainReplyKeyboard(t *testing.T) {
	msg := &tg.Message{
		ID: 1,
		ReplyMarkup: &tg.ReplyKeyboardMarkup{
			Resize:      true,
			SingleUse:   true,
			Selective:   true,
			Placeholder: "Pick one",
			Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButton{Text: "Yes"},
				&tg.KeyboardButtonRequestPhone{Text: "Share phone"},
			}}},
		},
	}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	require.NotNil(t, kb)
	assert.Equal(t, keyboardKindReply, kb.Keyboard)
	assert.True(t, kb.Resize)
	assert.True(t, kb.SingleUse)
	assert.True(t, kb.Selective)
	assert.Equal(t, "Pick one", kb.Placeholder)
	require.Len(t, kb.Rows[0], 2)
	assert.Equal(t, inlineButton{Text: "Yes", Type: buttonTypeReply}, kb.Rows[0][0])
	assert.Equal(t, buttonTypeRequestPhone, kb.Rows[0][1].Type)
	assert.Equal(t, "!tg click 1 0 1", kb.Rows[0][1].Command)
}

func TestConvertReplyMarkup_HideAndForceReply(t *testing.T) {
	hideMsg := &tg.Message{ID: 1, ReplyMarkup: &tg.ReplyKeyboardHide{Selective: true}}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, hideMsg, "!tg")
	require.NotNil(t, kb)
	assert.Equal(t, keyboardKindHide, kb.Keyboard)
	assert.True(t, kb.Selective)
	assert.Nil(t, kb.Rows)

	forceMsg := &tg.Message{ID: 1, ReplyMarkup: &tg.ReplyKeyboardForceReply{Placeholder: "Reply here"}}
	kb = convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, forceMsg, "!tg")
	require.NotNil(t, kb)
	assert.Equal(t, keyboardKindForceReply, kb.Keyboard)
	assert.Equal(t, "Reply here", kb.Placeholder)
}

func TestConvertReplyMarkup_SwitchInlineAndUserProfileUseResolver(t *testing.T) {
	msg := &tg.Message{
		ID: 1,
		ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonSwitchInline{Text: "Search", Query: "cats", SamePeer: true},
			&tg.KeyboardButtonUserProfile{Text: "Profile", UserID: 999},
		}}}},
	}
	res := &fakeButtonResolver{botUsername: "somebot", userMXID: "@telegram_999:example.com"}
	kb := convertReplyMarkupWith(context.Background(), res, msg, "!tg")
	require.NotNil(t, kb)
	assert.Equal(t, inlineButton{
		Text:        "Search",
		Type:        buttonTypeSwitchInline,
		Query:       "cats",
		SamePeer:    true,
		BotUsername: "somebot",
	}, kb.Rows[0][0])
	assert.Equal(t, inlineButton{
		Text:     "Profile",
		Type:     buttonTypeUserProfile,
		UserMXID: "@telegram_999:example.com",
	}, kb.Rows[0][1])
}

func TestConvertReplyMarkup_CopyButton(t *testing.T) {
	msg := &tg.Message{
		ID: 1,
		ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonCopy{Text: "Copy code", CopyText: "ABC123"},
		}}}},
	}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	require.NotNil(t, kb)
	assert.Equal(t, inlineButton{Text: "Copy code", Type: buttonTypeCopy, CopyText: "ABC123"}, kb.Rows[0][0])
}

func TestContentHashInput(t *testing.T) {
	var nilKB *inlineKeyboard
	assert.Nil(t, nilKB.contentHashInput())

	msgA := &tg.Message{ID: 1, ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonCallback{Text: "A", Data: []byte("a")},
	}}}}}
	msgB := &tg.Message{ID: 1, ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonCallback{Text: "B", Data: []byte("b")},
	}}}}}
	kbA := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msgA, "!tg")
	kbB := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msgB, "!tg")
	assert.NotEmpty(t, kbA.contentHashInput())
	assert.NotEqual(t, kbA.contentHashInput(), kbB.contentHashInput())
}

func TestFallbackText(t *testing.T) {
	msg := &tg.Message{
		ID: 1,
		ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "Docs", URL: "https://example.com"},
			&tg.KeyboardButtonCallback{Text: "Click me", Data: []byte("x")},
		}}}},
	}
	kb := convertReplyMarkupWith(context.Background(), &fakeButtonResolver{}, msg, "!tg")
	require.NotNil(t, kb)
	plain, htmlText := kb.fallbackText()
	assert.Contains(t, plain, "Docs (https://example.com)")
	assert.Contains(t, plain, "Click me (!tg click 1 0 1)")
	assert.Contains(t, htmlText, `<a href="https://example.com">Docs</a>`)
	assert.Contains(t, htmlText, "Click me")
	assert.Contains(t, htmlText, "!tg click 1 0 1")
}

func TestFallbackText_HideAndForceReplyHaveNoButtons(t *testing.T) {
	kb := &inlineKeyboard{Keyboard: keyboardKindHide}
	content := &event.MessageEventContent{MsgType: event.MsgNotice, Body: "original"}
	kb.appendFallbackText(content)
	assert.Equal(t, "original", content.Body)
}
