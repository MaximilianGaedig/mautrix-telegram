package connector

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

type fakeIntent struct {
	bridgev2.MatrixAPI
	mxid id.UserID
}

func (f fakeIntent) GetMXID() id.UserID { return f.mxid }

func TestLiveLocationEnded(t *testing.T) {
	start := 1_700_000_000
	at := func(secs int) time.Time { return time.Unix(int64(start+secs), 0) }
	assert.False(t, liveLocationEnded(start, 900, at(60)), "15-minute share, a minute in")
	assert.True(t, liveLocationEnded(start, 900, at(900)), "its period is over")
	// Stopping early edits the period down to the time it ran.
	assert.True(t, liveLocationEnded(start, 120, at(120)))
	assert.False(t, liveLocationEnded(start, liveLocationForever, at(365*24*3600)), "shared until stopped")
}

func TestLiveLocationTimeout(t *testing.T) {
	assert.Equal(t, 15*time.Minute, liveLocationTimeout(900))
	assert.Equal(t, maxLiveLocationTimeout, liveLocationTimeout(liveLocationForever))
	assert.Equal(t, maxLiveLocationTimeout, liveLocationTimeout(90*24*3600))
}

func TestConvertLiveLocation(t *testing.T) {
	msg := &tg.Message{ID: 5, Date: 1_700_000_000}
	live := &tg.MessageMediaGeoLive{Geo: &tg.GeoPoint{Lat: 52.5, Long: 13.4, AccuracyRadius: 20}, Period: 900}
	parts := convertLiveLocation(fakeIntent{mxid: "@telegram_1:example.org"}, msg, live)
	require.Len(t, parts, 2)

	info := parts[0]
	assert.Equal(t, event.StateUnstableBeaconInfo, info.Type)
	require.NotNil(t, info.StateKey)
	assert.Equal(t, "@telegram_1:example.org", *info.StateKey, "a beacon_info is keyed by its sharer")
	assert.Equal(t, true, info.Extra["live"])
	assert.Equal(t, int64(900_000), info.Extra["timeout"])
	require.NotNil(t, info.Content, "backfill sends the static location instead")
	assert.Equal(t, event.MsgLocation, info.Content.MsgType)

	beacon := parts[1]
	assert.Equal(t, event.EventUnstableBeacon, beacon.Type)
	assert.True(t, beacon.ReferencesPrevious)
	assert.Equal(t, map[string]any{"uri": "geo:52.500000,13.400000;u=20"}, beacon.Extra["org.matrix.msc3488.location"])

	assert.Nil(t, convertLiveLocation(fakeIntent{}, msg, &tg.MessageMediaGeoLive{Geo: &tg.GeoPointEmpty{}}))
}

func TestLiveLocationPart(t *testing.T) {
	info := &database.Message{PartID: "", Metadata: &MessageMetadata{LiveLocation: true}}
	beacon := &database.Message{PartID: "beacon", Metadata: &MessageMetadata{}}
	assert.Same(t, info, liveLocationPart([]*database.Message{beacon, info}))
	assert.Nil(t, liveLocationPart([]*database.Message{{Metadata: &MessageMetadata{}}}), "a static location")
}
