package queue

import (
    "context"
    "encoding/json"
    "fmt"

    "github.com/google/uuid"
    "github.com/nats-io/nats.go"
)

const linkSavedSubject = "keepstack.links.saved"

// Publisher publishes domain events to NATS.
type Publisher interface {
    PublishLinkSaved(ctx context.Context, linkID uuid.UUID) error
    Close()
}

// NATS wraps a nats.Conn to satisfy Publisher.
type NATS struct {
    conn *nats.Conn
}

// New creates a new NATS publisher connection.
func New(url string) (*NATS, error) {
    conn, err := nats.Connect(url, nats.Name("keepstack-api"))
    if err != nil {
        return nil, fmt.Errorf("connect to nats: %w", err)
    }
    return &NATS{conn: conn}, nil
}

// PublishLinkSaved emits a message indicating a link should be processed.
func (n *NATS) PublishLinkSaved(ctx context.Context, linkID uuid.UUID) error {
    payload := map[string]string{"link_id": linkID.String()}
    data, err := json.Marshal(payload)
    if err != nil {
        return fmt.Errorf("marshal link saved payload: %w", err)
    }

    return n.conn.PublishMsg(&nats.Msg{Subject: linkSavedSubject, Data: data})
}

// Close shuts down the underlying NATS connection.
func (n *NATS) Close() {
    if n.conn != nil {
        n.conn.Close()
    }
}
