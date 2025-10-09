package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

const (
	subjectLinksSaved = "keepstack.links.saved"
	queueGroup        = "keepstack-worker"
)

// LinkSavedMessage represents the payload emitted by the API when a link is stored.
type LinkSavedMessage struct {
	LinkID string `json:"link_id"`
}

// Handler processes incoming link saved events.
type Handler func(ctx context.Context, linkID uuid.UUID) error

// ReadyCallback is invoked after the subscriber successfully registers with
// NATS and is ready to receive messages.
type ReadyCallback func()

// Subscriber wraps a NATS connection for consuming events.
type Subscriber struct {
	conn *nats.Conn
}

// NewSubscriber connects to NATS and returns a subscriber instance.
func NewSubscriber(url string) (*Subscriber, error) {
	conn, err := nats.Connect(url, nats.Name("keepstack-worker"))
	if err != nil {
		return nil, fmt.Errorf("connect to nats: %w", err)
	}
	return &Subscriber{conn: conn}, nil
}

// Listen subscribes to link saved events until the context is cancelled.
func (s *Subscriber) Listen(ctx context.Context, handler Handler, ready ReadyCallback) error {
	sub, err := s.conn.QueueSubscribe(subjectLinksSaved, queueGroup, func(msg *nats.Msg) {
		var payload LinkSavedMessage
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			log.Printf("worker: invalid payload: %v", err)
			return
		}
		linkID, err := uuid.Parse(payload.LinkID)
		if err != nil {
			log.Printf("worker: invalid link id: %v", err)
			return
		}

		jobCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()

		if err := handler(jobCtx, linkID); err != nil {
			log.Printf("worker: handler error for %s: %v", linkID, err)
			return
		}

		if err := msg.Ack(); err != nil {
			// Ack only succeeds when JetStream is configured; ignore for core NATS.
			log.Printf("worker: ack warning: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe to subject: %w", err)
	}
	if err := s.conn.Flush(); err != nil {
		return err
	}

	if ready != nil {
		ready()
	}

	<-ctx.Done()
	return sub.Drain()
}

// Close shuts down the underlying connection.
func (s *Subscriber) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}
