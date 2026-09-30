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
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tgerr"
)

var _ bridgev2.PollHandlingNetworkAPI = (*TelegramClient)(nil)

const (
	pollKindDisclosed   = "org.matrix.msc3381.poll.disclosed"
	pollKindUndisclosed = "org.matrix.msc3381.poll.undisclosed"

	// Telegram's limits for polls created by users.
	minPollAnswers        = 2
	maxPollAnswers        = 10
	maxPollQuestionLength = 255
	maxPollAnswerLength   = 100

	// getPollVotes is asked for this many voters per page, and for at most this many pages.
	pollVotesPageSize = 100
	pollVotesMaxPages = 5

	pollVoteIDPrefix = "poll-vote:"
	pollEndIDPrefix  = "poll-end:"
)

// pollAnswerID is the Matrix answer ID for a Telegram poll option. Options are opaque bytes, so they are hex encoded.
func pollAnswerID(meta *PollMetadata, option []byte) string {
	if meta != nil {
		for answerID, opt := range meta.MatrixOptions {
			if bytes.Equal(opt, option) {
				return answerID
			}
		}
	}
	return hex.EncodeToString(option)
}

// pollOptionForAnswer is the Telegram option bytes for a Matrix answer ID.
func pollOptionForAnswer(meta *PollMetadata, answerID string) ([]byte, error) {
	if meta != nil {
		if opt, ok := meta.MatrixOptions[answerID]; ok {
			return opt, nil
		} else if len(meta.MatrixOptions) > 0 {
			return nil, fmt.Errorf("unknown poll answer %q", answerID)
		}
	}
	opt, err := hex.DecodeString(answerID)
	if err != nil || len(opt) == 0 {
		return nil, fmt.Errorf("unknown poll answer %q", answerID)
	}
	return opt, nil
}

// pollOptionsKey identifies a set of chosen options, independent of their order. It is empty for no options.
func pollOptionsKey(options [][]byte) string {
	keys := make([]string, len(options))
	for i, opt := range options {
		keys[i] = hex.EncodeToString(opt)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func pollAnswerIDs(meta *PollMetadata, options [][]byte) []string {
	answerIDs := make([]string, len(options))
	for i, opt := range options {
		answerIDs[i] = pollAnswerID(meta, opt)
	}
	return answerIDs
}

// pollMaxSelections is the number of answers one vote may pick, as Matrix polls state it.
func pollMaxSelections(poll *tg.Poll) int {
	if poll.MultipleChoice {
		return max(len(poll.Answers), 1)
	}
	return 1
}

// telegramPollMetadata is what gets saved along with the message that carries the poll.
func telegramPollMetadata(poll *tg.Poll) *PollMetadata {
	return &PollMetadata{
		MaxSelections: pollMaxSelections(poll),
		PublicVoters:  poll.PublicVoters,
		Closed:        poll.Closed,
	}
}

// telegramPollToMatrix converts a Telegram poll to the content of an org.matrix.msc3381.poll.start event. The
// text fallback is in the plain body and in the extensible event fields, for clients without poll support.
func telegramPollToMatrix(poll *tg.Poll) (*event.MessageEventContent, map[string]any) {
	question := poll.Question.Text
	answers := make([]map[string]any, 0, len(poll.Answers))
	var textAnswers []string
	var htmlAnswers strings.Builder
	for i, rawAnswer := range poll.Answers {
		answer, ok := rawAnswer.(*tg.PollAnswer)
		if !ok {
			continue
		}
		text := answer.Text.Text
		answers = append(answers, map[string]any{
			"id":                      hex.EncodeToString(answer.Option),
			"org.matrix.msc1767.text": text,
		})
		textAnswers = append(textAnswers, fmt.Sprintf("%d. %s", i+1, text))
		htmlAnswers.WriteString("<li>" + event.TextToHTML(text) + "</li>")
	}

	label := "Poll"
	if poll.Quiz {
		// Matrix polls have no right answer; the question says it's a quiz, and the end says the answer.
		label = "Quiz"
		question = "Quiz: " + question
	}
	body := fmt.Sprintf("%s: %s\n\n%s", label, poll.Question.Text, strings.Join(textAnswers, "\n"))
	formattedBody := fmt.Sprintf("<p><strong>%s</strong>: %s</p><ol>%s</ol>", label, event.TextToHTML(poll.Question.Text), htmlAnswers.String())
	if poll.Closed {
		body += "\n\n(This poll is closed.)"
		formattedBody += "<p>(This poll is closed.)</p>"
	}

	kind := pollKindDisclosed
	if poll.HideResultsUntilClose {
		kind = pollKindUndisclosed
	}
	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          body,
		Format:        event.FormatHTML,
		FormattedBody: formattedBody,
	}
	extra := map[string]any{
		"org.matrix.msc1767.message": []map[string]any{
			{"mimetype": "text/html", "body": formattedBody},
			{"mimetype": "text/plain", "body": body},
		},
		"org.matrix.msc3381.poll.start": map[string]any{
			"kind":           kind,
			"max_selections": pollMaxSelections(poll),
			"question": map[string]any{
				"org.matrix.msc1767.text": question,
			},
			"answers": answers,
		},
		"fi.mau.telegram.poll": map[string]any{
			"public_voters":   poll.PublicVoters,
			"multiple_choice": poll.MultipleChoice,
			"quiz":            poll.Quiz,
			"closed":          poll.Closed,
		},
	}
	return content, extra
}

// pollTopAnswers is the text of the answer with the most votes, or all of them on a tie. It's empty when nobody voted.
func pollTopAnswers(poll *tg.Poll, results *tg.PollResults) []string {
	if poll == nil || results == nil {
		return nil
	}
	best := 0
	var top []string
	for _, voters := range results.Results {
		if voters.Voters == 0 || voters.Voters < best {
			continue
		}
		var text string
		for _, rawAnswer := range poll.Answers {
			if answer, ok := rawAnswer.(*tg.PollAnswer); ok && bytes.Equal(answer.Option, voters.Option) {
				text = answer.Text.Text
			}
		}
		if text == "" {
			continue
		}
		if voters.Voters > best {
			best = voters.Voters
			top = top[:0]
		}
		top = append(top, text)
	}
	return top
}

// quizCorrectAnswer is the text of a quiz's right answer, once Telegram has told it (after voting or at the end).
func quizCorrectAnswer(poll *tg.Poll, results *tg.PollResults) string {
	if poll == nil || !poll.Quiz || results == nil {
		return ""
	}
	for _, voters := range results.Results {
		if !voters.Correct {
			continue
		}
		for _, rawAnswer := range poll.Answers {
			if answer, ok := rawAnswer.(*tg.PollAnswer); ok && bytes.Equal(answer.Option, voters.Option) {
				return answer.Text.Text
			}
		}
	}
	return ""
}

// pollEndContent is the content of the org.matrix.msc3381.poll.end event for a poll that was closed on Telegram.
func pollEndContent(pollEventID id.EventID, poll *tg.Poll, results *tg.PollResults) (*event.MessageEventContent, map[string]any) {
	text := "The poll has ended."
	switch top := pollTopAnswers(poll, results); len(top) {
	case 0:
	case 1:
		text += " Top answer: " + top[0]
	default:
		text += " Top answers: " + strings.Join(top, ", ")
	}
	if correct := quizCorrectAnswer(poll, results); correct != "" {
		text += " Correct answer: " + correct
		if results.Solution != "" {
			text += ". " + results.Solution
		}
	}
	return &event.MessageEventContent{
			MsgType:   event.MsgText,
			Body:      text,
			RelatesTo: &event.RelatesTo{Type: event.RelReference, EventID: pollEventID},
		}, map[string]any{
			"org.matrix.msc3381.poll.end": map[string]any{},
			"org.matrix.msc1767.text":     text,
		}
}

// pollResponseContent is the content of an org.matrix.msc3381.poll.response event. An empty list retracts the vote.
func pollResponseContent(pollEventID id.EventID, answerIDs []string) (*event.MessageEventContent, map[string]any) {
	if answerIDs == nil {
		answerIDs = []string{}
	}
	return &event.MessageEventContent{
			RelatesTo: &event.RelatesTo{Type: event.RelReference, EventID: pollEventID},
		}, map[string]any{
			"org.matrix.msc3381.poll.response": map[string]any{"answers": answerIDs},
		}
}

// matrixPollToTelegram converts a Matrix poll start event to what Telegram needs to create the poll. The map is
// the Matrix answer ID to Telegram option bytes mapping, which has to be saved to bridge votes later.
func matrixPollToTelegram(content *event.PollStartEventContent) (*tg.InputMediaPoll, map[string][]byte, error) {
	start := &content.PollStart
	question := strings.TrimSpace(start.Question.GetText())
	if question == "" {
		return nil, nil, errors.New("the poll has no question")
	} else if utf8.RuneCountInString(question) > maxPollQuestionLength {
		return nil, nil, fmt.Errorf("the poll question is longer than Telegram's limit of %d characters", maxPollQuestionLength)
	} else if len(start.Answers) < minPollAnswers {
		return nil, nil, fmt.Errorf("Telegram polls need at least %d answers", minPollAnswers)
	} else if len(start.Answers) > maxPollAnswers {
		return nil, nil, fmt.Errorf("Telegram polls can have at most %d answers", maxPollAnswers)
	}
	options := make(map[string][]byte, len(start.Answers))
	answers := make([]tg.PollAnswerClass, len(start.Answers))
	for i, answer := range start.Answers {
		text := strings.TrimSpace(answer.GetText())
		if text == "" {
			return nil, nil, fmt.Errorf("answer %d of the poll is empty", i+1)
		} else if utf8.RuneCountInString(text) > maxPollAnswerLength {
			return nil, nil, fmt.Errorf("answer %d of the poll is longer than Telegram's limit of %d characters", i+1, maxPollAnswerLength)
		} else if _, exists := options[answer.ID]; exists {
			return nil, nil, fmt.Errorf("the poll has two answers with the ID %q", answer.ID)
		}
		option := []byte(strconv.Itoa(i))
		options[answer.ID] = option
		answers[i] = &tg.PollAnswer{
			Text:   tg.TextWithEntities{Text: text},
			Option: option,
		}
	}
	return &tg.InputMediaPoll{
		Poll: tg.Poll{
			Question:       tg.TextWithEntities{Text: question},
			Answers:        answers,
			MultipleChoice: start.MaxSelections > 1,
			// Undisclosed polls hide the results until they're closed. Voters stay anonymous, as Telegram's default.
			HideResultsUntilClose: start.Kind == pollKindUndisclosed,
		},
	}, options, nil
}

// matrixPollMetadata is what gets saved with a poll that was started on Matrix.
func matrixPollMetadata(content *event.PollStartEventContent, options map[string][]byte) *PollMetadata {
	return &PollMetadata{
		MatrixOptions: options,
		MaxSelections: max(content.PollStart.MaxSelections, 1),
	}
}

// matrixVoteToOptions is the Telegram options a Matrix poll response picks. An empty result retracts the vote.
func matrixVoteToOptions(answers []string, meta *PollMetadata) ([][]byte, error) {
	var options [][]byte
	for _, answerID := range answers {
		opt, err := pollOptionForAnswer(meta, answerID)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(options, func(o []byte) bool { return bytes.Equal(o, opt) }) {
			options = append(options, opt)
		}
	}
	if meta != nil && meta.MaxSelections > 0 && len(options) > meta.MaxSelections {
		return nil, fmt.Errorf("the poll allows at most %d answers, but the vote has %d", meta.MaxSelections, len(options))
	}
	return options, nil
}

// pollOwnVote is the options the logged-in user has picked according to poll results. known is false when the
// results say nothing about it, which is the case for results that are shared between users (min) and for
// results that Telegram doesn't fill in until the poll is closed.
func pollOwnVote(results *tg.PollResults) (options [][]byte, known bool) {
	if results == nil || results.Min || len(results.Results) == 0 {
		return nil, false
	}
	for _, voters := range results.Results {
		if voters.Chosen {
			options = append(options, voters.Option)
		}
	}
	return options, true
}

// pollResultsSignature is a summary of the vote counts, used to tell whether a results update changed anything.
func pollResultsSignature(results *tg.PollResults) string {
	var sb strings.Builder
	sb.WriteString(strconv.Itoa(results.TotalVoters))
	for _, voters := range results.Results {
		sb.WriteString(";" + hex.EncodeToString(voters.Option) + "=" + strconv.Itoa(voters.Voters))
	}
	return sb.String()
}

// pollVote is the vote of one user, as listed by getPollVotes.
type pollVote struct {
	UserID  int64
	Options [][]byte
	Date    time.Time
}

// pollVotesFromList collects what getPollVotes returned into one vote per user, oldest first.
func pollVotesFromList(list []tg.MessagePeerVoteClass) []pollVote {
	byUser := map[int64]*pollVote{}
	var order []int64
	add := func(peer tg.PeerClass, date int, options ...[]byte) {
		user, ok := peer.(*tg.PeerUser)
		if !ok {
			return
		}
		vote, exists := byUser[user.UserID]
		if !exists {
			vote = &pollVote{UserID: user.UserID}
			byUser[user.UserID] = vote
			order = append(order, user.UserID)
		}
		for _, opt := range options {
			if !slices.ContainsFunc(vote.Options, func(o []byte) bool { return bytes.Equal(o, opt) }) {
				vote.Options = append(vote.Options, opt)
			}
		}
		if t := time.Unix(int64(date), 0); t.After(vote.Date) {
			vote.Date = t
		}
	}
	for _, raw := range list {
		switch v := raw.(type) {
		case *tg.MessagePeerVote:
			add(v.Peer, v.Date, v.Option)
		case *tg.MessagePeerVoteMultiple:
			add(v.Peer, v.Date, v.Options...)
		}
	}
	votes := make([]pollVote, 0, len(order))
	for _, userID := range order {
		votes = append(votes, *byUser[userID])
	}
	sort.SliceStable(votes, func(i, j int) bool { return votes[i].Date.Before(votes[j].Date) })
	return votes
}

// makePollVoteID is the bridge message ID of a vote. It never parses as a Telegram message ID. The date is part of
// it so that a user who changes their vote and then changes it back gets a new event instead of a duplicate.
func makePollVoteID(pollID networkid.MessageID, userID networkid.UserID, optionsKey string, date time.Time) networkid.MessageID {
	return networkid.MessageID(fmt.Sprintf("%s%s:%s:%s:%d", pollVoteIDPrefix, pollID, userID, optionsKey, date.UnixNano()))
}

func parsePollVoteID(messageID networkid.MessageID) (pollID networkid.MessageID, ok bool) {
	rest, ok := strings.CutPrefix(string(messageID), pollVoteIDPrefix)
	if !ok {
		return "", false
	}
	rawPollID, _, ok := strings.Cut(rest, ":")
	return networkid.MessageID(rawPollID), ok
}

// withFloodWait runs fn again after Telegram's flood wait, a few times at most.
func withFloodWait(ctx context.Context, fn func() error) error {
	var err error
	for attempts := 0; attempts < 5; attempts++ {
		var retry bool
		retry, err = tgerr.FloodWait(ctx, fn())
		if !retry {
			return err
		}
	}
	return err
}

func pollUnsupported(err error) error {
	return bridgev2.WrapErrorInStatus(err).
		WithErrorReason(event.MessageStatusUnsupported).
		WithIsCertain(true).
		WithSendNotice(true).
		WithErrorAsMessage()
}

func (tc *TelegramClient) HandleMatrixPollStart(ctx context.Context, msg *bridgev2.MatrixPollStart) (*bridgev2.MatrixMessageResponse, error) {
	tc.markActive(ctx)
	if msg.Portal.RoomType == database.RoomTypeSpace {
		return nil, fmt.Errorf("can't send messages to space portals")
	}
	media, options, err := matrixPollToTelegram(msg.Content)
	if err != nil {
		return nil, pollUnsupported(err)
	}
	// Handle Matrix events only after initial connection has been established to avoid deadlocking gotd
	if err = tc.clientInitialized.Wait(ctx); err != nil {
		return nil, err
	}
	peer, topicID, err := tc.inputPeerForPortalID(ctx, msg.Portal.ID)
	if err != nil {
		return nil, err
	}
	var replyTo tg.InputReplyToClass
	if msg.ReplyTo != nil {
		_, messageID, err := ids.ParseMessageID(msg.ReplyTo.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse replied-to message ID: %w", err)
		}
		replyTo = &tg.InputReplyToMessage{ReplyToMsgID: messageID}
	}
	if topicID > 0 {
		if replyTo == nil {
			replyTo = &tg.InputReplyToMessage{ReplyToMsgID: topicID}
		} else {
			replyTo.(*tg.InputReplyToMessage).TopMsgID = topicID
		}
	}
	randomID := parseRandomID(msg.InputTransactionID)
	var updates tg.UpdatesClass
	err = withFloodWait(ctx, func() (err error) {
		updates, err = tc.client.API().MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
			Peer:     peer,
			Media:    media,
			ReplyTo:  replyTo,
			RandomID: randomID,
		})
		return
	})
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to send poll to Telegram")
		return nil, tc.humaniseSendError(err)
	}
	resp, err := tc.makeSendResponse(ctx, &msg.MatrixMessage, updates, randomID, "", media.Poll.Question.Text)
	if err != nil {
		return nil, err
	}
	resp.DB.Metadata.(*MessageMetadata).Poll = matrixPollMetadata(msg.Content, options)
	return resp, nil
}

func (tc *TelegramClient) HandleMatrixPollVote(ctx context.Context, msg *bridgev2.MatrixPollVote) (*bridgev2.MatrixMessageResponse, error) {
	tc.markActive(ctx)
	if msg.Portal.RoomType == database.RoomTypeSpace {
		return nil, fmt.Errorf("can't send messages to space portals")
	}
	pollMeta, ok := msg.VoteTo.Metadata.(*MessageMetadata)
	if !ok || pollMeta.Poll == nil {
		return nil, pollUnsupported(errors.New("this message was bridged without poll data, so it can't be voted on"))
	}
	options, err := matrixVoteToOptions(msg.Content.Response.Answers, pollMeta.Poll)
	if err != nil {
		return nil, pollUnsupported(err)
	}
	if err = tc.sendPollVote(ctx, msg.Portal, msg.VoteTo, options); err != nil {
		return nil, err
	}
	now := time.Now()
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        makePollVoteID(msg.VoteTo.ID, tc.userID, pollOptionsKey(options), now),
			SenderID:  tc.userID,
			Timestamp: now,
			Metadata:  &MessageMetadata{},
		},
	}, nil
}

// sendPollVote votes on Telegram, and remembers the vote so that Telegram's update about it isn't bridged back.
func (tc *TelegramClient) sendPollVote(ctx context.Context, portal *bridgev2.Portal, pollMsg *database.Message, options [][]byte) error {
	if err := tc.clientInitialized.Wait(ctx); err != nil {
		return err
	}
	peer, _, err := tc.inputPeerForPortalID(ctx, portal.ID)
	if err != nil {
		return err
	}
	_, telegramMsgID, err := ids.ParseMessageID(pollMsg.ID)
	if err != nil {
		return err
	}
	if options == nil {
		options = [][]byte{}
	}
	err = withFloodWait(ctx, func() error {
		_, err := tc.client.API().MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{
			Peer:    peer,
			MsgID:   telegramMsgID,
			Options: options,
		})
		return err
	})
	if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to send poll vote to Telegram")
		return tc.humaniseSendError(err)
	}
	meta := pollMsg.Metadata.(*MessageMetadata)
	meta.Poll.OwnVote = pollOptionsKey(options)
	if err = tc.main.Bridge.DB.Message.Update(ctx, pollMsg); err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to save own poll vote")
	}
	return nil
}

// retractPollVote handles the redaction of a vote event on Matrix.
func (tc *TelegramClient) retractPollVote(ctx context.Context, portal *bridgev2.Portal, voteMsg *database.Message, pollID networkid.MessageID) error {
	if voteMsg.SenderID != tc.userID {
		return fmt.Errorf("can't retract the vote of another user")
	}
	pollMsg, err := tc.main.Bridge.DB.Message.GetFirstPartByID(ctx, portal.Receiver, pollID)
	if err != nil {
		return err
	} else if pollMsg == nil {
		return fmt.Errorf("poll of the vote not found")
	} else if meta, ok := pollMsg.Metadata.(*MessageMetadata); !ok || meta.Poll == nil {
		return fmt.Errorf("poll of the vote has no poll data")
	}
	return tc.sendPollVote(ctx, portal, pollMsg, nil)
}

// rememberPollMessage lets updateMessagePoll be matched to its message even when it doesn't say which one it's for.
func (tc *TelegramClient) rememberPollMessage(msg *tg.Message) {
	if media, ok := msg.GetMedia(); ok {
		if poll, ok := media.(*tg.MessageMediaPoll); ok && poll.Poll.ID != 0 {
			if messageID := ids.GetMessageIDFromMessage(msg); !tc.pollMessages.Replace(poll.Poll.ID, messageID) {
				tc.pollMessages.Push(poll.Poll.ID, messageID)
			}
		}
	}
}

func (tc *TelegramClient) onMessagePoll(ctx context.Context, update *tg.UpdateMessagePoll) error {
	var messageID networkid.MessageID
	if peer, ok := update.GetPeer(); ok {
		if msgID, ok := update.GetMsgID(); ok {
			messageID = ids.MakeMessageID(peer, msgID)
		}
	}
	if messageID == "" {
		var ok bool
		if messageID, ok = tc.pollMessages.Get(update.PollID); !ok {
			zerolog.Ctx(ctx).Debug().Int64("poll_id", update.PollID).Msg("Ignoring poll update for unknown message")
			return nil
		}
	}
	var poll *tg.Poll
	if p, ok := update.GetPoll(); ok {
		poll = &p
	}
	results := update.GetResults()
	return tc.syncPoll(ctx, messageID, poll, &results)
}

// syncPoll brings Matrix up to date with a poll after Telegram says its results changed: the votes that can be
// attributed to somebody become poll responses, and a poll that has been closed gets its end event.
func (tc *TelegramClient) syncPoll(ctx context.Context, messageID networkid.MessageID, poll *tg.Poll, results *tg.PollResults) error {
	log := zerolog.Ctx(ctx).With().Str("poll_message_id", string(messageID)).Logger()
	parts, err := tc.main.Bridge.DB.Message.GetAllPartsByID(ctx, tc.loginID, messageID)
	if err != nil {
		return fmt.Errorf("failed to get poll message: %w", err)
	} else if len(parts) == 0 {
		log.Debug().Msg("Ignoring poll update for message that hasn't been bridged")
		return nil
	}
	pollMsg := parts[0]
	meta, ok := pollMsg.Metadata.(*MessageMetadata)
	if !ok || meta.Poll == nil {
		// Polls that were bridged as plain text before polls were supported have nothing to vote on.
		log.Debug().Msg("Ignoring poll update for message without poll data")
		return nil
	}
	pm := meta.Poll
	closed := poll != nil && poll.Closed
	changed := false

	if options, known := pollOwnVote(results); known {
		if key := pollOptionsKey(options); key != pm.OwnVote {
			err = tc.queuePollVote(pollMsg, tc.mySender(), options, time.Now())
			if err != nil {
				return err
			}
			pm.OwnVote = key
			changed = true
		}
	}

	publicVoters := pm.PublicVoters
	if poll != nil {
		publicVoters = poll.PublicVoters
	}
	signature := pollResultsSignature(results)
	prevSignature, _ := tc.pollSignatures.Load(messageID)
	finalSync := closed && !pm.Closed
	if publicVoters && !tc.metadata.IsBot && (finalSync || prevSignature != signature) {
		if err = tc.syncPublicPollVotes(ctx, pollMsg); err != nil {
			// The own vote and the end event don't depend on this, so carry on.
			log.Err(err).Msg("Failed to sync poll voters")
		} else {
			tc.pollSignatures.Store(messageID, signature)
		}
	}

	if finalSync {
		tc.queuePollEnd(pollMsg.Room, messageID, tc.senderForPollMessage(pollMsg), poll, results)
		pm.Closed = true
		changed = true
	}
	if changed {
		if err = tc.main.Bridge.DB.Message.Update(ctx, pollMsg); err != nil {
			return fmt.Errorf("failed to save poll state: %w", err)
		}
	}
	return nil
}

func (tc *TelegramClient) senderForPollMessage(pollMsg *database.Message) bridgev2.EventSender {
	if pollMsg.SenderID == tc.userID {
		return tc.mySender()
	}
	sender := bridgev2.EventSender{Sender: pollMsg.SenderID}
	if peerType, userID, err := ids.ParseUserID(pollMsg.SenderID); err == nil && peerType == ids.PeerTypeUser {
		sender.SenderLogin = ids.MakeUserLoginID(userID)
	}
	return sender
}

// syncPublicPollVotes bridges the votes of other users on a poll with public voters. Votes are queued under an ID that
// contains the vote's date, so listing the same votes again is harmless. Votes that were retracted aren't listed
// by Telegram anymore, so they can't be noticed.
func (tc *TelegramClient) syncPublicPollVotes(ctx context.Context, pollMsg *database.Message) error {
	peer, _, err := tc.inputPeerForPortalID(ctx, pollMsg.Room.ID)
	if err != nil {
		return err
	}
	_, telegramMsgID, err := ids.ParseMessageID(pollMsg.ID)
	if err != nil {
		return err
	}
	var list []tg.MessagePeerVoteClass
	offset := ""
	for page := 0; page < pollVotesMaxPages; page++ {
		req := &tg.MessagesGetPollVotesRequest{
			Peer:   peer,
			ID:     telegramMsgID,
			Offset: offset,
			Limit:  pollVotesPageSize,
		}
		var resp *tg.MessagesVotesList
		err = withFloodWait(ctx, func() (err error) {
			resp, err = APICallWithOnlyUserUpdates(ctx, tc, func() (*tg.MessagesVotesList, error) {
				return tc.client.API().MessagesGetPollVotes(ctx, req)
			})
			return
		})
		if err != nil {
			return fmt.Errorf("failed to get poll votes: %w", err)
		}
		list = append(list, resp.Votes...)
		offset = resp.NextOffset
		if offset == "" || len(resp.Votes) == 0 {
			break
		}
	}
	for _, vote := range pollVotesFromList(list) {
		if vote.UserID == tc.telegramUserID {
			// The own vote is tracked from the poll results, which know about retractions too.
			continue
		}
		if err = tc.queuePollVote(pollMsg, tc.senderForUserID(vote.UserID), vote.Options, vote.Date); err != nil {
			return err
		}
	}
	return nil
}

func (tc *TelegramClient) queuePollVote(pollMsg *database.Message, sender bridgev2.EventSender, options [][]byte, date time.Time) error {
	pm := pollMsg.Metadata.(*MessageMetadata).Poll
	answerIDs := pollAnswerIDs(pm, options)
	res := tc.main.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Message[*database.Message]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.
					Str("action", "poll_vote").
					Str("poll_message_id", string(pollMsg.ID)).
					Str("sender", string(sender.Sender))
			},
			Sender:    sender,
			PortalKey: pollMsg.Room,
			Timestamp: date,
		},
		ID:   makePollVoteID(pollMsg.ID, sender.Sender, pollOptionsKey(options), date),
		Data: pollMsg,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, poll *database.Message) (*bridgev2.ConvertedMessage, error) {
			content, extra := pollResponseContent(poll.MXID, answerIDs)
			return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
				Type:       event.EventUnstablePollResponse,
				Content:    content,
				Extra:      extra,
				DBMetadata: &MessageMetadata{},
			}}}, nil
		},
	})
	return resultToError(res)
}

// queuePollEnd sends the end of a poll that was closed on Telegram. The start event is looked up when the event is
// handled, so this can be called right after the poll message itself was queued.
func (tc *TelegramClient) queuePollEnd(portalKey networkid.PortalKey, pollID networkid.MessageID, sender bridgev2.EventSender, poll *tg.Poll, results *tg.PollResults) {
	tc.main.Bridge.QueueRemoteEvent(tc.userLogin, &simplevent.Message[*tg.Poll]{
		EventMeta: simplevent.EventMeta{
			Type: bridgev2.RemoteEventMessage,
			LogContext: func(c zerolog.Context) zerolog.Context {
				return c.Str("action", "poll_end").Str("poll_message_id", string(pollID))
			},
			Sender:    sender,
			PortalKey: portalKey,
			Timestamp: time.Now(),
		},
		ID:   networkid.MessageID(pollEndIDPrefix + string(pollID)),
		Data: poll,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, poll *tg.Poll) (*bridgev2.ConvertedMessage, error) {
			pollMsg, err := tc.main.Bridge.DB.Message.GetFirstPartByID(ctx, portal.Receiver, pollID)
			if err != nil {
				return nil, fmt.Errorf("failed to get poll message: %w", err)
			} else if pollMsg == nil {
				return nil, fmt.Errorf("%w (poll message not found)", bridgev2.ErrIgnoringRemoteEvent)
			}
			content, extra := pollEndContent(pollMsg.MXID, poll, results)
			return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
				Type:       event.EventUnstablePollEnd,
				Content:    content,
				Extra:      extra,
				DBMetadata: &MessageMetadata{},
			}}}, nil
		},
	})
}

var _ bridgev2.PollEndHandlingNetworkAPI = (*TelegramClient)(nil)

// pollFromMessages finds the poll carried by message msgID among a getMessages result.
func pollFromMessages(messages []tg.MessageClass, msgID int) (*tg.Poll, error) {
	for _, m := range messages {
		msg, ok := m.(*tg.Message)
		if !ok || msg.ID != msgID {
			continue
		}
		media, ok := msg.Media.(*tg.MessageMediaPoll)
		if !ok {
			return nil, fmt.Errorf("message %d is not a poll", msgID)
		}
		return &media.Poll, nil
	}
	return nil, fmt.Errorf("poll message %d not found", msgID)
}

// closePollMedia is what stops a poll: Telegram only reads the poll's ID and the closed flag, the way
// tdlib's stopPoll sends it.
func closePollMedia(pollID int64) *tg.InputMediaPoll {
	poll := tg.Poll{ID: pollID}
	poll.SetClosed(true)
	return &tg.InputMediaPoll{Poll: poll}
}

func (tc *TelegramClient) HandleMatrixPollEnd(ctx context.Context, msg *bridgev2.MatrixPollEnd) error {
	tc.markActive(ctx)
	if err := tc.clientInitialized.Wait(ctx); err != nil {
		return err
	}
	meta, ok := msg.Poll.Metadata.(*MessageMetadata)
	if !ok || meta.Poll == nil {
		return fmt.Errorf("%w: the poll was bridged before polls were supported", bridgev2.ErrUnknownPoll)
	} else if meta.Poll.Closed {
		return nil
	}
	peerType, peerID, _, err := ids.ParsePortalID(msg.Portal.ID)
	if err != nil {
		return fmt.Errorf("failed to parse portal ID: %w", err)
	}
	_, telegramMsgID, err := ids.ParseMessageID(msg.Poll.ID)
	if err != nil {
		return err
	}
	// The poll's Telegram ID isn't kept with the message, so read it from the message itself.
	messages, err := tc.getMessagesByID(ctx, peerType, peerID, telegramMsgID)
	if err != nil {
		return fmt.Errorf("failed to fetch poll message: %w", err)
	}
	poll, err := pollFromMessages(messages.GetMessages(), telegramMsgID)
	if err != nil {
		return err
	}
	peer, _, err := tc.inputPeerForPortalID(ctx, msg.Portal.ID)
	if err != nil {
		return err
	}
	// Mark it closed first: Telegram echoes the close back as a poll update, which must not end the
	// poll in Matrix a second time.
	meta.Poll.Closed = true
	if err = tc.main.Bridge.DB.Message.Update(ctx, msg.Poll); err != nil {
		return fmt.Errorf("failed to save poll state: %w", err)
	}
	err = withFloodWait(ctx, func() error {
		_, err := tc.client.API().MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
			Peer:  peer,
			ID:    telegramMsgID,
			Media: closePollMedia(poll.ID),
		})
		return err
	})
	if err != nil {
		meta.Poll.Closed = false
		if dbErr := tc.main.Bridge.DB.Message.Update(ctx, msg.Poll); dbErr != nil {
			zerolog.Ctx(ctx).Err(dbErr).Msg("Failed to reset poll state after failing to close it")
		}
		return tc.humaniseSendError(err)
	}
	return nil
}
