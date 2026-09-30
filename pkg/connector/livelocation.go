package connector

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-telegram/pkg/gotd/tg"
)

// Telegram's live period for "until I turn it off".
const liveLocationForever = 0x7FFFFFFF

// A Matrix live location needs an end; an open-ended Telegram share is given this one.
const maxLiveLocationTimeout = 30 * 24 * time.Hour

const liveLocationDescription = "Live location"

// liveLocationTimeout is how long a share of period seconds lasts in Matrix.
func liveLocationTimeout(period int) time.Duration {
	if period <= 0 || period == liveLocationForever {
		return maxLiveLocationTimeout
	}
	return min(time.Duration(period)*time.Second, maxLiveLocationTimeout)
}

// liveLocationEnded reports whether a share started at date with period seconds is over at ts. Stopping a
// share early shortens its period to the time it ran, so an edit that stops it is over too.
func liveLocationEnded(date, period int, ts time.Time) bool {
	if period == liveLocationForever {
		return false
	}
	return !time.Unix(int64(date), 0).Add(time.Duration(period) * time.Second).After(ts)
}

func liveLocationGeoURI(live *tg.MessageMediaGeoLive) (string, bool) {
	point, ok := live.Geo.(*tg.GeoPoint)
	if !ok {
		return "", false
	}
	return event.GeoURI(point.Lat, point.Long, float64(point.AccuracyRadius)), true
}

// convertLiveLocation turns a live location that's still being shared into a Matrix live location: the
// beacon_info state event of the sharer, then its first position as a beacon referring to it. The static
// location is the fallback where state can't be sent (backfill).
func convertLiveLocation(intent bridgev2.MatrixAPI, msg *tg.Message, live *tg.MessageMediaGeoLive) []*bridgev2.ConvertedMessagePart {
	geoURI, ok := liveLocationGeoURI(live)
	if !ok {
		return nil
	}
	date := time.Unix(int64(msg.Date), 0)
	stateKey := intent.GetMXID().String()
	fallback := convertLocation(live)
	return []*bridgev2.ConvertedMessagePart{{
		Type:     event.StateUnstableBeaconInfo,
		StateKey: &stateKey,
		Content:  fallback.Content,
		Extra:    event.BeaconInfoContent(liveLocationDescription, true, date, liveLocationTimeout(live.Period)),
	}, {
		ID:                 "beacon",
		Type:               event.EventUnstableBeacon,
		ReferencesPrevious: true,
		Extra:              event.BeaconContent("", geoURI, date),
	}}
}

// updateLiveLocation bridges an edit of a live location: a new position becomes a beacon, and a share that
// ended is stopped by setting its beacon_info to not live. Neither is an m.replace edit.
func (tc *TelegramClient) updateLiveLocation(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, beaconInfo *database.Message, msg *tg.Message, live *tg.MessageMediaGeoLive) error {
	ts := time.Unix(int64(msg.EditDate), 0)
	if msg.EditDate == 0 {
		ts = time.Now()
	}
	if liveLocationEnded(msg.Date, live.Period, ts) {
		_, err := intent.SendState(ctx, portal.MXID, event.StateUnstableBeaconInfo, intent.GetMXID().String(), &event.Content{
			Raw: event.BeaconInfoContent(liveLocationDescription, false, time.Unix(int64(msg.Date), 0), liveLocationTimeout(live.Period)),
		}, ts)
		if err != nil {
			return fmt.Errorf("failed to stop live location: %w", err)
		}
		return fmt.Errorf("%w (live location stopped)", bridgev2.ErrIgnoringRemoteEvent)
	}
	geoURI, ok := liveLocationGeoURI(live)
	if !ok {
		return fmt.Errorf("%w (live location without a position)", bridgev2.ErrIgnoringRemoteEvent)
	}
	_, err := intent.SendMessage(ctx, portal.MXID, event.EventUnstableBeacon, &event.Content{
		Raw: event.BeaconContent(beaconInfo.MXID, geoURI, ts),
	}, &bridgev2.MatrixSendExtra{Timestamp: ts})
	if err != nil {
		return fmt.Errorf("failed to send live location update: %w", err)
	}
	return fmt.Errorf("%w (live location update sent as a beacon)", bridgev2.ErrIgnoringRemoteEvent)
}

// liveLocationPart is the beacon_info part of a message bridged as a Matrix live location, or nil for a
// location bridged before live locations were (a static location, whose edits stay edits).
func liveLocationPart(parts []*database.Message) *database.Message {
	for _, part := range parts {
		if meta, ok := part.Metadata.(*MessageMetadata); ok && meta.LiveLocation {
			return part
		}
	}
	return nil
}
