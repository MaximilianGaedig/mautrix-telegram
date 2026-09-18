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
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-telegram/pkg/connector/humanise"
	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tgerr"
)

// clickCommandName is the name of the command that presses inline buttons. The commands in the keyboards that are
// sent to Matrix use it too.
const clickCommandName = "click"

// clickTimeout is the maximum time to spend on a button press. Telegram gives up on bots that don't answer a
// callback query on its own after about 15 seconds (BOT_RESPONSE_TIMEOUT), this is only a safety net.
const clickTimeout = 45 * time.Second

const clickUsage = "Usage: `$cmdprefix " + clickCommandName + " <message ID> <row> <column>` (rows and columns start at 0)"

var cmdClick = &commands.FullHandler{
	Func: fnClick,
	Name: clickCommandName,
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionMisc,
		Description: "Press a button of a keyboard attached to a Telegram message",
		Args:        "<_message ID_> <_row_> <_column_>",
	},
	RequiresPortal: true,
	RequiresLogin:  true,
}

// clickError is a problem with the button press request itself, as opposed to a failure to talk to Telegram.
// The message is safe and useful to show to the user as is.
type clickError struct {
	message string
}

func (ce *clickError) Error() string {
	return ce.message
}

func newClickError(format string, args ...any) error {
	return &clickError{message: fmt.Sprintf(format, args...)}
}

type clickArgs struct {
	MessageID int
	Row       int
	Column    int
}

func parseClickNumber(name, raw string) (int, error) {
	digits := strings.TrimPrefix(raw, "-")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, fmt.Errorf("the %s must be a whole number, got %q", name, raw)
	} else if digits != raw {
		return 0, fmt.Errorf("the %s can't be negative, got %q", name, raw)
	}
	// Message IDs are 32-bit integers in the Telegram protocol. Anything bigger would be silently truncated
	// and press a button on some other message, so reject it here.
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("the %s is too large, got %q", name, raw)
	}
	return int(value), nil
}

// parseClickArgs parses and validates the arguments of the click command: the Telegram message ID
// followed by the zero-based row and column of the button.
func parseClickArgs(args []string) (clickArgs, error) {
	if len(args) != 3 {
		return clickArgs{}, fmt.Errorf("expected 3 arguments (message ID, row and column), got %d", len(args))
	}
	msgID, err := parseClickNumber("message ID", args[0])
	if err != nil {
		return clickArgs{}, err
	} else if msgID == 0 {
		return clickArgs{}, errors.New("the message ID must be positive")
	}
	row, err := parseClickNumber("row", args[1])
	if err != nil {
		return clickArgs{}, err
	}
	column, err := parseClickNumber("column", args[2])
	if err != nil {
		return clickArgs{}, err
	}
	return clickArgs{MessageID: msgID, Row: row, Column: column}, nil
}

// messageInPeer checks that a message belongs to the given chat. Message IDs of users and basic groups are shared
// between all of them, so fetching a message by ID alone can return a message from a different chat.
func messageInPeer(msg *tg.Message, peerType ids.PeerType, peerID int64) bool {
	switch peer := msg.PeerID.(type) {
	case *tg.PeerUser:
		return peerType == ids.PeerTypeUser && peer.UserID == peerID
	case *tg.PeerChat:
		return peerType == ids.PeerTypeChat && peer.ChatID == peerID
	case *tg.PeerChannel:
		return peerType == ids.PeerTypeChannel && peer.ChannelID == peerID
	default:
		return false
	}
}

// findClickTarget picks the requested message out of the fetched messages.
func findClickTarget(messages []tg.MessageClass, msgID int, peerType ids.PeerType, peerID int64) (*tg.Message, error) {
	for _, rawMsg := range messages {
		msg, ok := rawMsg.(*tg.Message)
		if !ok || msg.ID != msgID {
			continue
		} else if !messageInPeer(msg, peerType, peerID) {
			return nil, newClickError("Message %d is not in this chat", msgID)
		}
		return msg, nil
	}
	return nil, newClickError("Message %d was not found on Telegram, it may have been deleted", msgID)
}

// selectButton finds the button at the given position of the keyboard of a message.
func selectButton(msg *tg.Message, row, column int) (tg.KeyboardButtonClass, error) {
	var rows []tg.KeyboardButtonRow
	switch markup := msg.ReplyMarkup.(type) {
	case *tg.ReplyInlineMarkup:
		rows = markup.Rows
	case *tg.ReplyKeyboardMarkup:
		rows = markup.Rows
	}
	if len(rows) == 0 {
		return nil, newClickError("Message %d doesn't have any buttons (they may have been removed)", msg.ID)
	} else if row >= len(rows) {
		return nil, newClickError("There is no row %d, the message has %d row(s) of buttons", row, len(rows))
	}
	buttons := rows[row].Buttons
	if column >= len(buttons) {
		return nil, newClickError("There is no column %d, row %d has %d button(s)", column, row, len(buttons))
	}
	button := buttons[column]
	if button == nil {
		return nil, newClickError("There is no button at row %d, column %d", row, column)
	}
	return button, nil
}

// clickResult is what fnClick shows to the user after a button press. Exactly one of Text or Reacted should
// end up being used.
type clickResult struct {
	// Text is a message describing what happened, e.g. the bot's answer or a URL to open.
	Text string
	// NoFeedback means the button doesn't have a meaningful outcome to report (e.g. a plain reply-keyboard
	// button), so a checkmark reaction should be used instead of Text if Text is also empty.
}

// pressButton fetches the current message from Telegram and presses the button at the given position, dispatching
// to the right Telegram API call for the button's type. See docs/inline-buttons.md for what each type does.
func (tc *TelegramClient) pressButton(ctx context.Context, portalID networkid.PortalID, args clickArgs) (*clickResult, error) {
	ctx, cancel := context.WithTimeout(ctx, clickTimeout)
	defer cancel()

	peerType, peerID, _, err := ids.ParsePortalID(portalID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse portal ID: %w", err)
	}
	messages, err := tc.getMessagesByID(ctx, peerType, peerID, args.MessageID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch message %d: %w", args.MessageID, err)
	}
	msg, err := findClickTarget(messages.GetMessages(), args.MessageID, peerType, peerID)
	if err != nil {
		return nil, err
	}
	button, err := selectButton(msg, args.Row, args.Column)
	if err != nil {
		return nil, err
	}
	inputPeer, _, err := tc.inputPeerForPortalID(ctx, portalID)
	if err != nil {
		return nil, fmt.Errorf("failed to get input peer: %w", err)
	}

	switch typed := button.(type) {
	case *tg.KeyboardButtonCallback:
		return tc.pressCallbackButton(ctx, inputPeer, msg, typed, false)
	case *tg.KeyboardButtonGame:
		return tc.pressGameButton(ctx, inputPeer, msg)
	case *tg.KeyboardButtonURLAuth:
		return tc.pressURLAuthButton(ctx, inputPeer, msg, typed)
	case *tg.KeyboardButtonWebView:
		return tc.pressWebViewButton(ctx, inputPeer, msg, typed.URL)
	case *tg.KeyboardButtonSimpleWebView:
		return tc.pressSimpleWebViewButton(ctx, inputPeer, msg, typed.URL)
	case *tg.KeyboardButtonRequestPhone:
		return tc.pressRequestPhoneButton(ctx, inputPeer)
	case *tg.KeyboardButtonRequestGeoLocation:
		return &clickResult{Text: "This button asks for your location. Send a location message to this chat instead; the bridge can't do it for you."}, nil
	case *tg.KeyboardButtonRequestPoll:
		return &clickResult{Text: "This button asks you to create a poll. Send a poll to this chat instead; the bridge can't do it for you."}, nil
	case *tg.KeyboardButtonRequestPeer:
		return &clickResult{Text: "This button asks you to choose and share a chat, which isn't supported through the bridge. Use the official Telegram app to press it."}, nil
	case *tg.KeyboardButtonURL:
		return nil, newClickError("That is a link button, open %s instead", typed.URL)
	case *tg.KeyboardButtonCopy:
		return nil, newClickError("That is a copy-to-clipboard button (%q); your Matrix client should offer that without pressing anything", typed.CopyText)
	case *tg.KeyboardButtonSwitchInline:
		return nil, newClickError("That button switches to inline bot mode, which isn't supported through the bridge")
	case *tg.KeyboardButtonUserProfile:
		return nil, newClickError("That button just links to a user profile; your Matrix client should offer that without pressing anything")
	case *tg.KeyboardButton:
		return nil, newClickError("That is a plain reply keyboard button; send %q as a normal message instead of using the click command", typed.Text)
	default:
		return nil, newClickError("That button (%s) can't be pressed through the bridge", button.TypeName())
	}
}

// pressCallbackButton presses a keyboardButtonCallback or (with game=true) the Game button, via
// messages.getBotCallbackAnswer.
func (tc *TelegramClient) pressCallbackButton(ctx context.Context, peer tg.InputPeerClass, msg *tg.Message, button *tg.KeyboardButtonCallback, game bool) (*clickResult, error) {
	if button.RequiresPassword {
		return nil, newClickError("That button needs your Telegram 2FA password, which can't be entered through the bridge")
	}
	data := button.Data
	if data == nil {
		data = []byte{}
	}
	answer, err := tc.client.API().MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{
		Game:  game,
		Peer:  peer,
		MsgID: msg.ID,
		Data:  data,
	})
	if err != nil {
		return nil, err
	}
	return &clickResult{Text: describeCallbackAnswer(answer)}, nil
}

// pressGameButton presses a keyboardButtonGame. Games use the same RPC as callback buttons, just with the Game
// flag set and no data; the bot is expected to answer with a URL to the game.
func (tc *TelegramClient) pressGameButton(ctx context.Context, peer tg.InputPeerClass, msg *tg.Message) (*clickResult, error) {
	answer, err := tc.client.API().MessagesGetBotCallbackAnswer(ctx, &tg.MessagesGetBotCallbackAnswerRequest{
		Game:  true,
		Peer:  peer,
		MsgID: msg.ID,
	})
	if err != nil {
		return nil, err
	}
	if text := describeCallbackAnswer(answer); text != "" {
		return &clickResult{Text: text}, nil
	}
	return &clickResult{Text: "The bot didn't return a game URL."}, nil
}

// pressURLAuthButton presses a keyboardButtonUrlAuth by requesting and then accepting the URL authorization,
// which is the API's way of confirming the button press before the target URL includes the user's identity.
// There is no way to show the user the confirmation dialog that Telegram apps normally show before accepting, so
// this always accepts without requesting write access to avoid granting the bot anything beyond what a plain
// visit to the URL would.
func (tc *TelegramClient) pressURLAuthButton(ctx context.Context, peer tg.InputPeerClass, msg *tg.Message, button *tg.KeyboardButtonURLAuth) (*clickResult, error) {
	fallback := &clickResult{Text: "Open: " + button.URL}
	requested, err := tc.client.API().MessagesRequestURLAuth(ctx, &tg.MessagesRequestURLAuthRequest{
		Peer:     peer,
		MsgID:    msg.ID,
		ButtonID: button.ButtonID,
	})
	if err != nil {
		return nil, err
	}
	if _, ok := requested.(*tg.URLAuthResultDefault); ok {
		// The bot doesn't want to authorize the user, only open the plain URL
		return fallback, nil
	}
	accepted, err := tc.client.API().MessagesAcceptURLAuth(ctx, &tg.MessagesAcceptURLAuthRequest{
		WriteAllowed: false,
		Peer:         peer,
		MsgID:        msg.ID,
		ButtonID:     button.ButtonID,
	})
	if err != nil {
		return nil, err
	}
	if result, ok := accepted.(*tg.URLAuthResultAccepted); ok && result.URL != "" {
		return &clickResult{Text: "Open: " + result.URL}, nil
	}
	return fallback, nil
}

// getBotInputUser resolves the InputUser of the bot behind a message, needed for the WebView RPCs.
func (tc *TelegramClient) getBotInputUser(ctx context.Context, msg *tg.Message) (tg.InputUserClass, error) {
	botID, ok := resolveBotID(msg)
	if !ok {
		return nil, newClickError("Couldn't figure out which bot owns this button")
	}
	accessHash, err := tc.ScopedStore.GetAccessHash(ctx, ids.PeerTypeUser, botID)
	if err != nil {
		return nil, fmt.Errorf("failed to get bot access hash: %w", err)
	}
	return &tg.InputUser{UserID: botID, AccessHash: accessHash}, nil
}

// pressWebViewButton presses a keyboardButtonWebView via messages.requestWebView. Telegram Mini Apps rely on a
// JavaScript API (Telegram.WebApp) that is only injected by real Telegram clients, so the page opened this way
// will generally not behave like it does inside Telegram; see docs/inline-buttons.md.
func (tc *TelegramClient) pressWebViewButton(ctx context.Context, peer tg.InputPeerClass, msg *tg.Message, buttonURL string) (*clickResult, error) {
	bot, err := tc.getBotInputUser(ctx, msg)
	if err != nil {
		return nil, err
	}
	result, err := tc.client.API().MessagesRequestWebView(ctx, &tg.MessagesRequestWebViewRequest{
		Peer: peer,
		Bot:  bot,
		URL:  buttonURL,
		ReplyTo: &tg.InputReplyToMessage{
			ReplyToMsgID: msg.ID,
		},
	})
	if err != nil {
		return nil, err
	}
	return &clickResult{Text: "Open (won't behave like it does inside Telegram, see the bridge docs): " + result.URL}, nil
}

// pressSimpleWebViewButton presses a keyboardButtonSimpleWebView via messages.requestSimpleWebView. Same caveat
// about Telegram's WebApp JS API as pressWebViewButton.
func (tc *TelegramClient) pressSimpleWebViewButton(ctx context.Context, peer tg.InputPeerClass, msg *tg.Message, buttonURL string) (*clickResult, error) {
	bot, err := tc.getBotInputUser(ctx, msg)
	if err != nil {
		return nil, err
	}
	result, err := tc.client.API().MessagesRequestSimpleWebView(ctx, &tg.MessagesRequestSimpleWebViewRequest{
		Bot: bot,
		URL: buttonURL,
	})
	if err != nil {
		return nil, err
	}
	return &clickResult{Text: "Open (won't behave like it does inside Telegram, see the bridge docs): " + result.URL}, nil
}

// pressRequestPhoneButton presses a keyboardButtonRequestPhone by sending the logged-in user's own contact card
// to the chat, which is what Telegram apps do when this button is pressed.
func (tc *TelegramClient) pressRequestPhoneButton(ctx context.Context, peer tg.InputPeerClass) (*clickResult, error) {
	if tc.metadata.LoginPhone == "" {
		return nil, newClickError("Your Telegram account doesn't have a phone number on file to share")
	}
	firstName := "Telegram User"
	if ghost, err := tc.main.Bridge.GetGhostByID(ctx, tc.userID); err == nil && ghost != nil && ghost.Name != "" {
		firstName = ghost.Name
	}
	_, err := tc.client.API().MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer: peer,
		Media: &tg.InputMediaContact{
			PhoneNumber: tc.metadata.LoginPhone,
			FirstName:   firstName,
		},
		RandomID: rand.Int64(),
	})
	if err != nil {
		return nil, err
	}
	return &clickResult{Text: "Shared your contact info."}, nil
}

// describeCallbackAnswer turns the answer of a bot into the text that is sent to the user.
// It is empty if the bot answered without any text or link, which is the case for most buttons.
func describeCallbackAnswer(answer *tg.MessagesBotCallbackAnswer) string {
	var lines []string
	if answer.Message != "" {
		if answer.Alert {
			// Alerts are pop-ups in Telegram apps that the bot wants the user to notice
			lines = append(lines, "Alert from the bot: "+answer.Message)
		} else {
			lines = append(lines, "Answer from the bot: "+answer.Message)
		}
	}
	if answer.URL != "" {
		lines = append(lines, "The bot asked you to open: "+answer.URL)
	}
	return strings.Join(lines, "\n")
}

// humaniseClickError turns an error from pressing a button into a message for the user.
func humaniseClickError(err error) string {
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return fmt.Sprintf("Telegram is rate limiting you, try again in %d seconds", int(math.Ceil(wait.Seconds())))
	}
	switch {
	case tg.IsBotResponseTimeout(err):
		return humanise.Error(err) + ". The bot may still have handled the button press, check the chat for changes."
	case tgerr.IsCode(err, -503), errors.Is(err, context.DeadlineExceeded):
		return "Timed out while waiting for the bot to answer. The bot may still have handled the button press, check the chat for changes."
	default:
		return "Failed to press the button: " + humanise.Error(err)
	}
}

func fnClick(ce *commands.Event) {
	args, err := parseClickArgs(ce.Args)
	if err != nil {
		ce.Reply("%s.\n\n"+clickUsage, err)
		return
	}
	login, _, err := ce.Portal.FindPreferredLogin(ce.Ctx, ce.User, false)
	if errors.Is(err, bridgev2.ErrNotLoggedIn) {
		ce.Reply("You need to be logged in to Telegram to press buttons in this chat")
		return
	} else if err != nil {
		ce.Log.Err(err).Msg("Failed to find preferred login for click command")
		ce.Reply("Failed to find a login to press the button with.")
		return
	}
	client, ok := login.Client.(*TelegramClient)
	if !ok {
		ce.Reply("Unexpected client type for the login")
		return
	} else if client.metadata.IsBot {
		ce.Reply("Bot accounts can't press buttons")
		return
	}

	log := ce.Log.With().
		Int("message_id", args.MessageID).
		Int("row", args.Row).
		Int("column", args.Column).
		Logger()
	result, err := client.pressButton(ce.Ctx, ce.Portal.ID, args)
	var userErr *clickError
	if errors.As(err, &userErr) {
		log.Debug().Err(err).Msg("Rejected button press")
		ce.ReplyAdvanced(userErr.Error(), false, false)
		return
	} else if err != nil {
		log.Warn().Err(err).Msg("Failed to press button")
		ce.ReplyAdvanced(humaniseClickError(err), false, false)
		return
	}
	log.Debug().Bool("has_text", result.Text != "").Msg("Pressed button")
	// The bot's answer is untrusted text, so send it without any formatting
	if result.Text != "" {
		ce.ReplyAdvanced(result.Text, false, false)
	} else if ce.React("✅️") == "" {
		ce.ReplyAdvanced("Button pressed.", false, false)
	}
}
