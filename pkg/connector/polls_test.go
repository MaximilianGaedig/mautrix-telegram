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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func testTelegramPoll() *tg.Poll {
	return &tg.Poll{
		Question: tg.TextWithEntities{Text: "Best fruit?"},
		Answers: []tg.PollAnswerClass{
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Apple"}, Option: []byte("0")},
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Pear <b>"}, Option: []byte("1")},
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Plum"}, Option: []byte("2")},
		},
	}
}

func testMatrixPoll(maxSelections int, kind string) *event.PollStartEventContent {
	answer := func(id, text string) event.PollOption {
		return event.PollOption{ID: id, MSC1767Message: event.MSC1767Message{Text: text}}
	}
	return &event.PollStartEventContent{PollStart: event.PollStart{
		Kind:          kind,
		MaxSelections: maxSelections,
		Question:      event.MSC1767Message{Text: "Lunch spot?"},
		Answers:       []event.PollOption{answer("a-1", "Noodles"), answer("a-2", "Tacos"), answer("a-3", "Soup")},
	}}
}

func TestTelegramPollToMatrix(t *testing.T) {
	poll := testTelegramPoll()
	poll.MultipleChoice = true
	poll.HideResultsUntilClose = true
	poll.PublicVoters = true

	content, extra := telegramPollToMatrix(poll)

	start, ok := extra["org.matrix.msc3381.poll.start"].(map[string]any)
	require.True(t, ok, "the poll start field is missing")
	assert.Equal(t, pollKindUndisclosed, start["kind"])
	assert.Equal(t, 3, start["max_selections"])
	assert.Equal(t, map[string]any{"org.matrix.msc1767.text": "Best fruit?"}, start["question"])
	answers := start["answers"].([]map[string]any)
	require.Len(t, answers, 3)
	assert.Equal(t, "30", answers[0]["id"], "answer IDs are the hex of the Telegram option bytes")
	assert.Equal(t, "Pear <b>", answers[1]["org.matrix.msc1767.text"])

	assert.Contains(t, content.Body, "Best fruit?")
	assert.Contains(t, content.Body, "2. Pear <b>")
	assert.NotContains(t, content.Body, "Open the Telegram app")
	assert.Contains(t, content.FormattedBody, "Pear &lt;b&gt;", "answers are escaped in the HTML fallback")
	assert.Equal(t, true, extra["fi.mau.telegram.poll"].(map[string]any)["public_voters"])

	_, singleExtra := telegramPollToMatrix(testTelegramPoll())
	singleStart := singleExtra["org.matrix.msc3381.poll.start"].(map[string]any)
	assert.Equal(t, 1, singleStart["max_selections"])
	assert.Equal(t, pollKindDisclosed, singleStart["kind"])
}

func TestTelegramPollToMatrixClosed(t *testing.T) {
	poll := testTelegramPoll()
	poll.Closed = true

	content, extra := telegramPollToMatrix(poll)

	assert.Contains(t, content.Body, "closed")
	assert.Equal(t, true, extra["fi.mau.telegram.poll"].(map[string]any)["closed"])
	assert.True(t, telegramPollMetadata(poll).Closed)
	assert.False(t, telegramPollMetadata(testTelegramPoll()).Closed)
}

func TestPollEndContent(t *testing.T) {
	poll := testTelegramPoll()
	results := &tg.PollResults{Results: []tg.PollAnswerVoters{
		{Option: []byte("0"), Voters: 2},
		{Option: []byte("1"), Voters: 5},
		{Option: []byte("2"), Voters: 5},
	}}

	content, extra := pollEndContent("$poll:example.com", poll, results)

	assert.Equal(t, event.RelReference, content.RelatesTo.Type)
	assert.Equal(t, "$poll:example.com", string(content.RelatesTo.EventID))
	assert.Contains(t, extra, "org.matrix.msc3381.poll.end")
	assert.Equal(t, "The poll has ended. Top answers: Pear <b>, Plum", content.Body)

	content, _ = pollEndContent("$poll:example.com", poll, &tg.PollResults{})
	assert.Equal(t, "The poll has ended.", content.Body)
}

func TestMatrixPollToTelegram(t *testing.T) {
	media, options, err := matrixPollToTelegram(testMatrixPoll(2, pollKindUndisclosed))
	require.NoError(t, err)

	assert.Equal(t, "Lunch spot?", media.Poll.Question.Text)
	assert.True(t, media.Poll.MultipleChoice)
	assert.True(t, media.Poll.HideResultsUntilClose)
	assert.False(t, media.Poll.PublicVoters, "voters stay anonymous")
	require.Len(t, media.Poll.Answers, 3)
	second := media.Poll.Answers[1].(*tg.PollAnswer)
	assert.Equal(t, "Tacos", second.Text.Text)
	assert.Equal(t, []byte("1"), second.Option)
	assert.Equal(t, map[string][]byte{"a-1": []byte("0"), "a-2": []byte("1"), "a-3": []byte("2")}, options)

	media, _, err = matrixPollToTelegram(testMatrixPoll(1, pollKindDisclosed))
	require.NoError(t, err)
	assert.False(t, media.Poll.MultipleChoice)
	assert.False(t, media.Poll.HideResultsUntilClose)
}

func TestMatrixPollToTelegramRejectsInvalid(t *testing.T) {
	noQuestion := testMatrixPoll(1, pollKindDisclosed)
	noQuestion.PollStart.Question = event.MSC1767Message{}
	oneAnswer := testMatrixPoll(1, pollKindDisclosed)
	oneAnswer.PollStart.Answers = oneAnswer.PollStart.Answers[:1]
	longAnswer := testMatrixPoll(1, pollKindDisclosed)
	longAnswer.PollStart.Answers[0].Text = strings.Repeat("x", maxPollAnswerLength+1)
	duplicateID := testMatrixPoll(1, pollKindDisclosed)
	duplicateID.PollStart.Answers[1].ID = "a-1"

	for name, content := range map[string]*event.PollStartEventContent{
		"no question": noQuestion, "one answer": oneAnswer, "long answer": longAnswer, "duplicate id": duplicateID,
	} {
		_, _, err := matrixPollToTelegram(content)
		assert.Error(t, err, name)
	}
}

func TestMatrixVoteToOptions(t *testing.T) {
	_, options, err := matrixPollToTelegram(testMatrixPoll(2, pollKindDisclosed))
	require.NoError(t, err)
	meta := matrixPollMetadata(testMatrixPoll(2, pollKindDisclosed), options)

	chosen, err := matrixVoteToOptions([]string{"a-3", "a-1", "a-3"}, meta)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("2"), []byte("0")}, chosen, "duplicates are dropped")

	chosen, err = matrixVoteToOptions(nil, meta)
	require.NoError(t, err)
	assert.Empty(t, chosen, "an empty vote retracts")

	_, err = matrixVoteToOptions([]string{"nope"}, meta)
	assert.Error(t, err, "unknown answer")
	_, err = matrixVoteToOptions([]string{"a-1", "a-2", "a-3"}, meta)
	assert.Error(t, err, "more answers than the poll allows")

	// Polls that started on Telegram use the hex of the option bytes.
	tgMeta := telegramPollMetadata(testTelegramPoll())
	chosen, err = matrixVoteToOptions([]string{"32"}, tgMeta)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("2")}, chosen)
	_, err = matrixVoteToOptions([]string{"zz"}, tgMeta)
	assert.Error(t, err)

	assert.Equal(t, []string{"a-2"}, pollAnswerIDs(meta, [][]byte{[]byte("1")}))
	assert.Equal(t, []string{"31"}, pollAnswerIDs(tgMeta, [][]byte{[]byte("1")}))
}

func TestPollOwnVote(t *testing.T) {
	results := &tg.PollResults{Results: []tg.PollAnswerVoters{
		{Option: []byte("0"), Voters: 3},
		{Option: []byte("1"), Voters: 1, Chosen: true},
	}}
	options, known := pollOwnVote(results)
	assert.True(t, known)
	assert.Equal(t, [][]byte{[]byte("1")}, options)

	results.Results[1].Chosen = false
	options, known = pollOwnVote(results)
	assert.True(t, known)
	assert.Empty(t, options, "no chosen option means the vote was retracted")

	results.Min = true
	_, known = pollOwnVote(results)
	assert.False(t, known, "min results don't say what the user chose")
	_, known = pollOwnVote(&tg.PollResults{})
	assert.False(t, known, "results that are hidden say nothing either")
}

func TestPollVotesFromList(t *testing.T) {
	list := []tg.MessagePeerVoteClass{
		&tg.MessagePeerVote{Peer: &tg.PeerUser{UserID: 11}, Option: []byte("0"), Date: 200},
		&tg.MessagePeerVoteMultiple{Peer: &tg.PeerUser{UserID: 12}, Options: [][]byte{[]byte("1"), []byte("2")}, Date: 100},
		&tg.MessagePeerVote{Peer: &tg.PeerUser{UserID: 11}, Option: []byte("2"), Date: 210},
		&tg.MessagePeerVote{Peer: &tg.PeerChannel{ChannelID: 5}, Option: []byte("0"), Date: 50},
		&tg.MessagePeerVoteInputOption{Peer: &tg.PeerUser{UserID: 13}, Date: 60},
	}

	votes := pollVotesFromList(list)

	require.Len(t, votes, 2, "channels and votes without options are skipped")
	assert.Equal(t, int64(12), votes[0].UserID, "oldest first")
	assert.Equal(t, [][]byte{[]byte("1"), []byte("2")}, votes[0].Options)
	assert.Equal(t, int64(11), votes[1].UserID)
	assert.Equal(t, [][]byte{[]byte("0"), []byte("2")}, votes[1].Options, "one user's options are merged")
	assert.Equal(t, time.Unix(210, 0), votes[1].Date)

	assert.Equal(t, pollOptionsKey([][]byte{[]byte("2"), []byte("0")}), pollOptionsKey([][]byte{[]byte("0"), []byte("2")}))
	assert.Equal(t, "", pollOptionsKey(nil))
}

func TestPollVoteID(t *testing.T) {
	pollID := ids.MakeMessageID(int64(77), 1234)
	voteID := makePollVoteID(pollID, ids.MakeUserID(11), "30,32", time.Unix(500, 0))

	parsedPoll, ok := parsePollVoteID(voteID)
	assert.True(t, ok)
	assert.Equal(t, pollID, parsedPoll)
	_, _, err := ids.ParseMessageID(voteID)
	assert.Error(t, err, "a vote must never be mistaken for a Telegram message")
	_, ok = parsePollVoteID(pollID)
	assert.False(t, ok)

	other := makePollVoteID(pollID, ids.MakeUserID(11), "30,32", time.Unix(501, 0))
	assert.NotEqual(t, voteID, other, "changing a vote back and forth gives new IDs")
}

func TestPollResponseContent(t *testing.T) {
	content, extra := pollResponseContent("$poll:example.com", nil)
	assert.Equal(t, "$poll:example.com", string(content.RelatesTo.EventID))
	assert.Equal(t, map[string]any{"answers": []string{}}, extra["org.matrix.msc3381.poll.response"])
}
