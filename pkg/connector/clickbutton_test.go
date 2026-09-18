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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-telegram/pkg/connector/ids"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestParseClickArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    clickArgs
		wantErr bool
	}{
		{name: "valid", args: []string{"12345", "0", "0"}, want: clickArgs{MessageID: 12345, Row: 0, Column: 0}},
		{name: "valid nonzero row col", args: []string{"7", "2", "3"}, want: clickArgs{MessageID: 7, Row: 2, Column: 3}},
		{name: "too few args", args: []string{"12345", "0"}, wantErr: true},
		{name: "too many args", args: []string{"12345", "0", "0", "0"}, wantErr: true},
		{name: "non-numeric message id", args: []string{"abc", "0", "0"}, wantErr: true},
		{name: "non-numeric row", args: []string{"1", "x", "0"}, wantErr: true},
		{name: "non-numeric column", args: []string{"1", "0", "x"}, wantErr: true},
		{name: "negative message id", args: []string{"-1", "0", "0"}, wantErr: true},
		{name: "negative row", args: []string{"1", "-1", "0"}, wantErr: true},
		{name: "negative column", args: []string{"1", "0", "-1"}, wantErr: true},
		{name: "zero message id", args: []string{"0", "0", "0"}, wantErr: true},
		{name: "message id too large", args: []string{"99999999999999999999", "0", "0"}, wantErr: true},
		{name: "empty message id", args: []string{"", "0", "0"}, wantErr: true},
		{name: "float message id", args: []string{"1.5", "0", "0"}, wantErr: true},
		{name: "row zero is fine", args: []string{"1", "0", "5"}, want: clickArgs{MessageID: 1, Row: 0, Column: 5}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseClickArgs(test.args)
			if test.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestMessageInPeer(t *testing.T) {
	tests := []struct {
		name     string
		msg      *tg.Message
		peerType ids.PeerType
		peerID   int64
		want     bool
	}{
		{
			name:     "user match",
			msg:      &tg.Message{PeerID: &tg.PeerUser{UserID: 5}},
			peerType: ids.PeerTypeUser,
			peerID:   5,
			want:     true,
		},
		{
			name:     "user mismatch",
			msg:      &tg.Message{PeerID: &tg.PeerUser{UserID: 5}},
			peerType: ids.PeerTypeUser,
			peerID:   6,
			want:     false,
		},
		{
			name:     "channel match",
			msg:      &tg.Message{PeerID: &tg.PeerChannel{ChannelID: 42}},
			peerType: ids.PeerTypeChannel,
			peerID:   42,
			want:     true,
		},
		{
			name:     "wrong peer type",
			msg:      &tg.Message{PeerID: &tg.PeerChat{ChatID: 42}},
			peerType: ids.PeerTypeChannel,
			peerID:   42,
			want:     false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, messageInPeer(test.msg, test.peerType, test.peerID))
		})
	}
}

func TestFindClickTarget(t *testing.T) {
	messages := []tg.MessageClass{
		&tg.MessageEmpty{ID: 1},
		&tg.Message{ID: 2, PeerID: &tg.PeerUser{UserID: 100}},
	}

	msg, err := findClickTarget(messages, 2, ids.PeerTypeUser, 100)
	require.NoError(t, err)
	assert.Equal(t, 2, msg.ID)

	_, err = findClickTarget(messages, 2, ids.PeerTypeUser, 200)
	assert.Error(t, err)
	var clickErr *clickError
	assert.ErrorAs(t, err, &clickErr)

	_, err = findClickTarget(messages, 999, ids.PeerTypeUser, 100)
	assert.Error(t, err)
	assert.ErrorAs(t, err, &clickErr)
}

func TestSelectButton(t *testing.T) {
	msg := &tg.Message{
		ID: 1,
		ReplyMarkup: &tg.ReplyInlineMarkup{
			Rows: []tg.KeyboardButtonRow{
				{Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonCallback{Text: "A", Data: []byte("a")}}},
			},
		},
	}

	button, err := selectButton(msg, 0, 0)
	require.NoError(t, err)
	callback, ok := button.(*tg.KeyboardButtonCallback)
	require.True(t, ok)
	assert.Equal(t, "A", callback.Text)

	_, err = selectButton(msg, 1, 0)
	assert.Error(t, err)
	var clickErr *clickError
	assert.ErrorAs(t, err, &clickErr)

	_, err = selectButton(msg, 0, 1)
	assert.Error(t, err)
	assert.ErrorAs(t, err, &clickErr)

	_, err = selectButton(&tg.Message{ID: 2}, 0, 0)
	assert.Error(t, err)
	assert.ErrorAs(t, err, &clickErr)
}

func TestDescribeCallbackAnswer(t *testing.T) {
	assert.Empty(t, describeCallbackAnswer(&tg.MessagesBotCallbackAnswer{}))
	assert.Equal(t, "Answer from the bot: hi", describeCallbackAnswer(&tg.MessagesBotCallbackAnswer{Message: "hi"}))
	assert.Equal(t, "Alert from the bot: hi", describeCallbackAnswer(&tg.MessagesBotCallbackAnswer{Message: "hi", Alert: true}))
	answer := describeCallbackAnswer(&tg.MessagesBotCallbackAnswer{Message: "hi", URL: "https://example.com"})
	assert.Contains(t, answer, "hi")
	assert.Contains(t, answer, "https://example.com")
}

func TestResolveBotID(t *testing.T) {
	viaBot := &tg.Message{}
	viaBot.SetViaBotID(555)
	id, ok := resolveBotID(viaBot)
	assert.True(t, ok)
	assert.Equal(t, int64(555), id)

	fromBot := &tg.Message{}
	fromBot.SetFromID(&tg.PeerUser{UserID: 777})
	id, ok = resolveBotID(fromBot)
	assert.True(t, ok)
	assert.Equal(t, int64(777), id)

	_, ok = resolveBotID(&tg.Message{})
	assert.False(t, ok)
}
