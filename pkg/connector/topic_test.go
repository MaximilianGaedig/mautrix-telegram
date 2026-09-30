package connector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-telegram/pkg/gotd/bin"
	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

func TestMarkUnreadRequest(t *testing.T) {
	for _, unread := range []bool{true, false} {
		var buf bin.Buffer
		require.NoError(t, markUnreadRequest(&tg.InputPeerSelf{}, unread).Encode(&buf))
		var decoded tg.MessagesMarkDialogUnreadRequest
		require.NoError(t, decoded.Decode(&buf))
		// The flag is what Telegram reads: an unset one marks the chat read.
		assert.Equal(t, unread, decoded.Unread)
	}
}
