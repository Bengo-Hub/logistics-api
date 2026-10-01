// Package realtime holds the WebSocket write loop shared by logistics-api's hubs. The cross-
// replica registry and relay live in shared-events (FanoutHub); this is only the socket side.
package realtime

import (
	"context"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"nhooyr.io/websocket"
)

const (
	// WriteTimeout bounds every frame write, so a stalled client cannot hold the loop.
	WriteTimeout = 5 * time.Second
	// PingInterval keeps idle connections alive through proxies and detects dead peers.
	PingInterval = 25 * time.Second
)

// Pump serves one WebSocket connection from a FanoutHub subscription until the client
// disconnects or ctx ends: it sends hello (if non-nil) first, then every message from sub.C,
// pings on an interval, and discards client frames (reading is what detects a close).
func Pump(ctx context.Context, conn *websocket.Conn, sub *eventslib.Sub, hello []byte) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		defer cancel()
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	write := func(b []byte) error {
		wctx, wcancel := context.WithTimeout(ctx, WriteTimeout)
		defer wcancel()
		return conn.Write(wctx, websocket.MessageText, b)
	}
	if hello != nil {
		if err := write(hello); err != nil {
			return
		}
	}
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.C:
			if !ok {
				return
			}
			if err := write(msg); err != nil {
				return
			}
		case <-ticker.C:
			pctx, pcancel := context.WithTimeout(ctx, WriteTimeout)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		}
	}
}
