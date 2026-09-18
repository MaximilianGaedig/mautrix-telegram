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
	"fmt"
	"html"
	"net/url"
	"strings"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// inlineButtonsField is the key of the custom Matrix event content field that carries the keyboard of a Telegram
// message. For edits, it is part of `m.new_content` (the bridgev2 Extra map), so an edit that removes the keyboard
// simply doesn't have the field.
//
// See docs/inline-buttons.md for the schema.
const inlineButtonsField = "fi.mau.telegram.buttons"

// Keyboard kinds, i.e. the top-level "keyboard" field.
const (
	keyboardKindInline     = "inline"
	keyboardKindReply      = "reply"
	keyboardKindHide       = "hide"
	keyboardKindForceReply = "force_reply"
)

// Button types, i.e. the per-button "type" field.
const (
	buttonTypeCallback      = "callback"
	buttonTypeURL           = "url"
	buttonTypeReply         = "reply"
	buttonTypeCopy          = "copy"
	buttonTypeSwitchInline  = "switch_inline"
	buttonTypeUserProfile   = "user_profile"
	buttonTypeURLAuth       = "url_auth"
	buttonTypeGame          = "game"
	buttonTypeWebView       = "webview"
	buttonTypeSimpleWebView = "simple_webview"
	buttonTypeRequestPhone  = "request_phone"
	buttonTypeRequestGeo    = "request_geo"
	buttonTypeRequestPoll   = "request_poll"
	buttonTypeRequestPeer   = "request_peer"
	buttonTypeUnsupported   = "unsupported"
)

// inlineKeyboard is the value of the inlineButtonsField content field. Despite the name (kept for API stability),
// it covers every kind of Telegram reply markup, not just inline keyboards.
type inlineKeyboard struct {
	// MessageID is the Telegram message ID the keyboard is attached to. It's the first argument of the click command.
	MessageID int `json:"message_id"`
	// Keyboard is one of keyboardKind*: what kind of markup this is.
	Keyboard string `json:"keyboard"`
	// Rows contains every row of the original Telegram keyboard, including buttons that can't be used. This keeps
	// the indexes in click commands valid: they always refer to the position in the original Telegram keyboard.
	// Empty/omitted for the "hide" and "force_reply" kinds, which don't have buttons.
	Rows [][]inlineButton `json:"rows,omitempty"`

	// The following fields only apply to "reply" and "force_reply" keyboards.
	Resize      bool   `json:"resize,omitempty"`
	SingleUse   bool   `json:"single_use,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Selective   bool   `json:"selective,omitempty"`
}

type inlineButton struct {
	Text string `json:"text"`
	Type string `json:"type"`
	// Command is the full Matrix message to send to trigger this button, when applicable.
	Command string `json:"command,omitempty"`
	// URL is the target of a url/url_auth button.
	URL string `json:"url,omitempty"`
	// RequiresPassword is set on callback buttons that require the Telegram 2FA password (SRP), which the click
	// command can't provide. The button still gets a command, but pressing it always fails with a clear error.
	RequiresPassword bool `json:"requires_password,omitempty"`
	// CopyText is the text a copy button puts on the clipboard. Handled entirely client-side.
	CopyText string `json:"copy_text,omitempty"`
	// Query and SamePeer are for switch_inline buttons.
	Query    string `json:"query,omitempty"`
	SamePeer bool   `json:"same_peer,omitempty"`
	// BotUsername is the bot to switch to inline mode with, if it could be resolved.
	BotUsername string `json:"bot_username,omitempty"`
	// UserMXID is the ghost Matrix user ID of the target of a user_profile button, if it could be resolved.
	UserMXID string `json:"user_mxid,omitempty"`
}

// commandPrefix returns the command prefix that is configured for the bridge.
func (tc *TelegramConnector) commandPrefix() string {
	if tc.Bridge != nil && tc.Bridge.Config != nil && tc.Bridge.Config.CommandPrefix != "" {
		return tc.Bridge.Config.CommandPrefix
	}
	return tc.GetName().DefaultCommandPrefix
}

// clickCommand builds the Matrix message that presses the button at the given position of a message.
func clickCommand(commandPrefix string, msgID, row, col int) string {
	return fmt.Sprintf("%s %s %d %d %d", commandPrefix, clickCommandName, msgID, row, col)
}

// isUsableButtonURL checks that the URL of a Telegram button is safe to hand to a Matrix client as a link.
// Only http(s) and Telegram deep links are allowed, so things like javascript: URLs can't sneak through a bot.
func isUsableButtonURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "tg":
		return true
	default:
		return false
	}
}

// resolveBotID finds the Telegram user ID of the bot that is "behind" a message: the inline bot that provided it,
// or (best-effort) the sender if the message looks like it's directly from a bot.
func resolveBotID(msg *tg.Message) (int64, bool) {
	if viaBotID, ok := msg.GetViaBotID(); ok && viaBotID != 0 {
		return viaBotID, true
	}
	if fromID, ok := msg.GetFromID(); ok {
		if peerUser, ok := fromID.(*tg.PeerUser); ok {
			return peerUser.UserID, true
		}
	}
	return 0, false
}

// resolveBotUsername tries to look up the username of the bot behind a message, for switch_inline buttons.
// It's best-effort: an empty string means the client should fall back to just the query without an @mention.
func (tc *TelegramClient) resolveBotUsername(ctx context.Context, msg *tg.Message) string {
	botID, ok := resolveBotID(msg)
	if !ok {
		return ""
	}
	username, err := tc.main.Store.Username.Get(ctx, ids.PeerTypeUser, botID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Int64("bot_id", botID).Msg("Failed to look up bot username for switch_inline button")
		return ""
	}
	return username
}

// resolveUserMXID looks up the ghost Matrix user ID for a user_profile button target.
func (tc *TelegramClient) resolveUserMXID(ctx context.Context, userID int64) string {
	ghost, err := tc.main.Bridge.GetGhostByID(ctx, ids.MakeUserID(userID))
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Int64("user_id", userID).Msg("Failed to get ghost for user_profile button")
		return ""
	}
	return ghost.Intent.GetMXID().String()
}

// buttonResolver looks up extra information needed to convert some button types. *TelegramClient implements it
// via resolveBotUsername/resolveUserMXID; tests can use a fake implementation instead.
type buttonResolver interface {
	resolveBotUsername(ctx context.Context, msg *tg.Message) string
	resolveUserMXID(ctx context.Context, userID int64) string
}

// convertButton converts a single Telegram keyboard button (from any kind of keyboard) into its Matrix
// representation. See docs/inline-buttons.md for what each button type means and its degree of support.
func convertButton(ctx context.Context, res buttonResolver, msg *tg.Message, button tg.KeyboardButtonClass, commandPrefix string, row, col int) inlineButton {
	if button == nil {
		return inlineButton{Type: buttonTypeUnsupported}
	}
	switch typed := button.(type) {
	case *tg.KeyboardButton:
		return inlineButton{Text: typed.Text, Type: buttonTypeReply}
	case *tg.KeyboardButtonURL:
		if isUsableButtonURL(typed.URL) {
			return inlineButton{Text: typed.Text, Type: buttonTypeURL, URL: typed.URL}
		}
	case *tg.KeyboardButtonCallback:
		return inlineButton{
			Text:             typed.Text,
			Type:             buttonTypeCallback,
			Command:          clickCommand(commandPrefix, msg.ID, row, col),
			RequiresPassword: typed.RequiresPassword,
		}
	case *tg.KeyboardButtonURLAuth:
		if isUsableButtonURL(typed.URL) {
			return inlineButton{
				Text:    typed.Text,
				Type:    buttonTypeURLAuth,
				URL:     typed.URL,
				Command: clickCommand(commandPrefix, msg.ID, row, col),
			}
		}
	case *tg.KeyboardButtonCopy:
		return inlineButton{Text: typed.Text, Type: buttonTypeCopy, CopyText: typed.CopyText}
	case *tg.KeyboardButtonSwitchInline:
		return inlineButton{
			Text:        typed.Text,
			Type:        buttonTypeSwitchInline,
			Query:       typed.Query,
			SamePeer:    typed.SamePeer,
			BotUsername: res.resolveBotUsername(ctx, msg),
		}
	case *tg.KeyboardButtonUserProfile:
		return inlineButton{
			Text:     typed.Text,
			Type:     buttonTypeUserProfile,
			UserMXID: res.resolveUserMXID(ctx, typed.UserID),
		}
	case *tg.KeyboardButtonGame:
		return inlineButton{Text: typed.Text, Type: buttonTypeGame, Command: clickCommand(commandPrefix, msg.ID, row, col)}
	case *tg.KeyboardButtonWebView:
		return inlineButton{Text: typed.Text, Type: buttonTypeWebView, Command: clickCommand(commandPrefix, msg.ID, row, col)}
	case *tg.KeyboardButtonSimpleWebView:
		return inlineButton{Text: typed.Text, Type: buttonTypeSimpleWebView, Command: clickCommand(commandPrefix, msg.ID, row, col)}
	case *tg.KeyboardButtonRequestPhone:
		return inlineButton{Text: typed.Text, Type: buttonTypeRequestPhone, Command: clickCommand(commandPrefix, msg.ID, row, col)}
	case *tg.KeyboardButtonRequestGeoLocation:
		return inlineButton{Text: typed.Text, Type: buttonTypeRequestGeo, Command: clickCommand(commandPrefix, msg.ID, row, col)}
	case *tg.KeyboardButtonRequestPoll:
		return inlineButton{Text: typed.Text, Type: buttonTypeRequestPoll, Command: clickCommand(commandPrefix, msg.ID, row, col)}
	case *tg.KeyboardButtonRequestPeer:
		return inlineButton{Text: typed.Text, Type: buttonTypeRequestPeer, Command: clickCommand(commandPrefix, msg.ID, row, col)}
	}
	// KeyboardButtonBuy (payments) and anything else (including URL/URLAuth buttons with an unusable URL)
	return inlineButton{Text: button.GetText(), Type: buttonTypeUnsupported}
}

// convertReplyMarkupWith converts the reply markup of a Telegram message into the keyboard that is sent to Matrix.
// It returns nil for messages that don't have any reply markup, or an inline/reply keyboard without any buttons.
func convertReplyMarkupWith(ctx context.Context, res buttonResolver, msg *tg.Message, commandPrefix string) *inlineKeyboard {
	switch markup := msg.ReplyMarkup.(type) {
	case *tg.ReplyInlineMarkup:
		return convertRowsMarkup(ctx, res, msg, markup.Rows, commandPrefix, keyboardKindInline, false, false, "", false)
	case *tg.ReplyKeyboardMarkup:
		return convertRowsMarkup(ctx, res, msg, markup.Rows, commandPrefix, keyboardKindReply, markup.Resize, markup.SingleUse, markup.Placeholder, markup.Selective)
	case *tg.ReplyKeyboardHide:
		return &inlineKeyboard{MessageID: msg.ID, Keyboard: keyboardKindHide, Selective: markup.Selective}
	case *tg.ReplyKeyboardForceReply:
		return &inlineKeyboard{
			MessageID:   msg.ID,
			Keyboard:    keyboardKindForceReply,
			SingleUse:   markup.SingleUse,
			Placeholder: markup.Placeholder,
			Selective:   markup.Selective,
		}
	default:
		return nil
	}
}

// convertReplyMarkup is the entry point used by the connector; it uses tc itself to resolve bot usernames and
// ghost MXIDs.
func (tc *TelegramClient) convertReplyMarkup(ctx context.Context, msg *tg.Message, commandPrefix string) *inlineKeyboard {
	return convertReplyMarkupWith(ctx, tc, msg, commandPrefix)
}

func convertRowsMarkup(
	ctx context.Context, res buttonResolver, msg *tg.Message, rawRows []tg.KeyboardButtonRow, commandPrefix, kind string,
	resize, singleUse bool, placeholder string, selective bool,
) *inlineKeyboard {
	keyboard := &inlineKeyboard{
		MessageID:   msg.ID,
		Keyboard:    kind,
		Rows:        make([][]inlineButton, len(rawRows)),
		Resize:      resize,
		SingleUse:   singleUse,
		Placeholder: placeholder,
		Selective:   selective,
	}
	buttonCount := 0
	for rowIndex, row := range rawRows {
		keyboard.Rows[rowIndex] = make([]inlineButton, len(row.Buttons))
		for colIndex, button := range row.Buttons {
			keyboard.Rows[rowIndex][colIndex] = convertButton(ctx, res, msg, button, commandPrefix, rowIndex, colIndex)
			buttonCount++
		}
	}
	if buttonCount == 0 {
		return nil
	}
	return keyboard
}

// contentHashInput returns the bytes that should be added to the content hash of a message with this keyboard.
// It's empty for messages without a keyboard, so their hashes stay the same as before buttons were bridged.
//
// The hash is what decides whether an edit is bridged at all: bots often edit only the keyboard, which would be
// dropped as a no-op edit if the keyboard wasn't part of the hash.
func (kb *inlineKeyboard) contentHashInput() []byte {
	if kb == nil {
		return nil
	}
	data, err := json.Marshal(kb)
	if err != nil {
		// Can't happen: the keyboard only consists of strings, bools and ints
		return nil
	}
	return append([]byte("\x00inline-buttons\x00"), data...)
}

func compactButtonLabel(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return "(no label)"
	}
	return text
}

// fallbackText renders the keyboard as a trailing paragraph for the message body, one line per row. URL buttons
// are links, buttons with a command are followed by that command, and everything else just shows its label.
func (kb *inlineKeyboard) fallbackText() (plain, htmlText string) {
	var plainBuilder, htmlBuilder strings.Builder
	plainBuilder.WriteString("Buttons:")
	htmlBuilder.WriteString("<p>Buttons:")
	for _, row := range kb.Rows {
		if len(row) == 0 {
			continue
		}
		plainBuilder.WriteByte('\n')
		htmlBuilder.WriteString("<br>")
		for i, button := range row {
			if i > 0 {
				plainBuilder.WriteString(" | ")
				htmlBuilder.WriteString(" | ")
			}
			label := compactButtonLabel(button.Text)
			escapedLabel := html.EscapeString(label)
			switch {
			case button.Type == buttonTypeURL || button.Type == buttonTypeURLAuth:
				_, _ = fmt.Fprintf(&plainBuilder, "%s (%s)", label, button.URL)
				_, _ = fmt.Fprintf(&htmlBuilder, `<a href="%s">%s</a>`, html.EscapeString(button.URL), escapedLabel)
			case button.Command != "":
				_, _ = fmt.Fprintf(&plainBuilder, "%s (%s)", label, button.Command)
				_, _ = fmt.Fprintf(&htmlBuilder, "%s (<code>%s</code>)", escapedLabel, html.EscapeString(button.Command))
			default:
				plainBuilder.WriteString(label)
				htmlBuilder.WriteString(escapedLabel)
			}
		}
	}
	htmlBuilder.WriteString("</p>")
	return plainBuilder.String(), htmlBuilder.String()
}

// appendFallbackText adds the text version of the keyboard to the end of the message content.
func (kb *inlineKeyboard) appendFallbackText(content *event.MessageEventContent) {
	// Hide/force_reply markups don't have buttons to render as a text fallback.
	if kb.Keyboard != keyboardKindInline && kb.Keyboard != keyboardKindReply {
		return
	}
	plain, htmlText := kb.fallbackText()
	// For media messages without a caption, this makes the fallback text the caption instead of mangling the file name.
	content.EnsureHasHTML()
	if content.Body != "" {
		content.Body += "\n\n"
	}
	content.Body += plain
	content.FormattedBody += htmlText
}

// applyInlineKeyboard adds the keyboard to the given part of a converted message. It does nothing if keyboard is nil,
// which is how an edit that removes the keyboard ends up without the field in the new content.
func applyInlineKeyboard(part *bridgev2.ConvertedMessagePart, keyboard *inlineKeyboard, textFallback bool) {
	if keyboard == nil {
		return
	}
	if part.Extra == nil {
		part.Extra = make(map[string]any)
	}
	part.Extra[inlineButtonsField] = keyboard
	// Stickers can't have a text body, so they only get the field.
	if textFallback && part.Content != nil && part.Type != event.EventSticker {
		keyboard.appendFallbackText(part.Content)
	}
}
