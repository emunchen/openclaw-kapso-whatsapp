package gateway

import (
	"context"
	"fmt"

	"github.com/Enriquefft/openclaw-kapso-whatsapp/internal/config"
)

// Gateway is the abstraction for AI agent backends (OpenClaw, ZeroClaw, etc.).
type Gateway interface {
	// Connect establishes a connection to the backend.
	Connect(ctx context.Context) error

	// SendAndReceive sends a message and blocks until the agent's reply is
	// available. The returned string is the raw agent response text.
	SendAndReceive(ctx context.Context, req *Request) (string, error)

	// Close tears down the connection.
	Close() error
}

// ImageAttachment holds downloaded image bytes for multimodal gateway messages.
type ImageAttachment struct {
	Data     []byte // raw image bytes
	MimeType string // e.g. "image/jpeg"
}

// Request carries all fields a gateway implementation might need to format and
// route a message. Each implementation picks the fields it cares about.
type Request struct {
	SessionKey     string            // agent session to target
	SessionsJSON   string            // per-request sessions.json path (multi-agent routing)
	IdempotencyKey string            // dedup key (typically the WhatsApp message ID)
	From           string            // sender phone number (E.164)
	FromName       string            // sender display name
	Role           string            // sender role (admin, member, etc.)
	Text           string            // raw message text
	Images         []ImageAttachment // image attachments (empty for text-only messages)
}

// New creates the appropriate Gateway for the configured type.
func New(cfg config.GatewayConfig) (Gateway, error) {
	switch cfg.Type {
	case "", "openclaw":
		return NewOpenClaw(cfg), nil
	case "zeroclaw":
		return NewZeroClaw(cfg), nil
	default:
		return nil, fmt.Errorf("unknown gateway type: %q", cfg.Type)
	}
}
