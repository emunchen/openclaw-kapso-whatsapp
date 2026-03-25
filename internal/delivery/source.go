package delivery

import (
	"context"
)

// ImageAttachment holds downloaded image bytes ready for gateway forwarding.
type ImageAttachment struct {
	Data     []byte // raw image bytes
	MimeType string // e.g. "image/jpeg"
}

// Event represents a single inbound message ready for the gateway.
type Event struct {
	ID     string            // Kapso message ID (idempotency key)
	From   string            // sender phone
	Name   string            // contact display name
	Text   string            // extracted, gateway-ready text
	Images []ImageAttachment // downloaded image data (empty for non-image messages)
}

// Source produces inbound message events from a delivery channel (poller, webhook, etc.).
type Source interface {
	Run(ctx context.Context, out chan<- Event) error
}
