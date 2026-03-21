package events

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/bengobox/logistics-service/internal/config"
)

func Connect(cfg config.EventsConfig) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("logistics-service"),
		nats.Timeout(5 * time.Second),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
	}

	return nats.Connect(cfg.NATSURL, opts...)
}

func EnsureStream(ctx context.Context, nc *nats.Conn, cfg config.EventsConfig) error {
	if nc == nil {
		return fmt.Errorf("nats connection is nil")
	}

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream init: %w", err)
	}

	desiredSubjects := []string{"logistics.>"}

	info, err := js.StreamInfo(cfg.StreamName)
	if err == nil {
		// Stream exists — update subjects if they changed (e.g. "logistics.*" → "logistics.>")
		if len(info.Config.Subjects) != len(desiredSubjects) || info.Config.Subjects[0] != desiredSubjects[0] {
			info.Config.Subjects = desiredSubjects
			if _, updateErr := js.UpdateStream(&info.Config); updateErr != nil {
				return fmt.Errorf("update stream subjects: %w", updateErr)
			}
		}
		return nil
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     cfg.StreamName,
		Subjects: desiredSubjects,
		Replicas: 1,
	})
	return err
}

