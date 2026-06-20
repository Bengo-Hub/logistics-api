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

// EnsureTenantStream guarantees a JetStream stream covers the cross-service
// "tenant.>" subject space (e.g. tenant.purge emitted by subscriptions-api on a
// platform-owner-confirmed dormancy purge). The owning service does not include
// "tenant.>" in its own stream, so a downstream consumer must make sure a stream
// retains the subject before binding a durable to it — otherwise the message is
// dropped at publish time ("no responders") and the purge never arrives.
//
// Idempotent and conflict-safe: if a stream named "tenant" already exists (or
// another service already created an overlapping stream and AddStream conflicts),
// it returns nil so startup is never blocked. The durable bind via js.Subscribe
// then attaches to whichever stream carries the subject.
func EnsureTenantStream(ctx context.Context, nc *nats.Conn) error {
	if nc == nil {
		return fmt.Errorf("nats connection is nil")
	}

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("jetstream init: %w", err)
	}

	const streamName = "tenant"
	if _, infoErr := js.StreamInfo(streamName); infoErr == nil {
		return nil // already present
	}

	if _, addErr := js.AddStream(&nats.StreamConfig{
		Name:     streamName,
		Subjects: []string{"tenant.>"},
		Replicas: 1,
	}); addErr != nil {
		// A concurrent creator (another service or pod) may have won the race or an
		// overlapping stream may already own the subject. Treat as best-effort: the
		// consumer's js.Subscribe will still bind to whatever stream retains it.
		return fmt.Errorf("ensure tenant stream: %w", addErr)
	}
	return nil
}

