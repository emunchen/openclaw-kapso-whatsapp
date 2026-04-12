package poller

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Enriquefft/openclaw-kapso-whatsapp/internal/delivery"
	"github.com/Enriquefft/openclaw-kapso-whatsapp/internal/kapso"
	"github.com/Enriquefft/openclaw-kapso-whatsapp/internal/transcribe"
)

// maxMediaRetries is the number of poll cycles to wait for a media URL before
// giving up and forwarding the message as text-only.
const maxMediaRetries = 6 // ~60s at 10s poll interval

// Poller implements delivery.Source by polling the Kapso list-messages API.
type Poller struct {
	Client       *kapso.Client
	Interval     time.Duration
	StateDir     string
	StateFile    string
	Transcriber  transcribe.Transcriber // nil = transcription disabled
	MaxAudioSize int64

	pendingMedia map[string]int // msgID → retry count for media messages without URL
}

// Run polls the Kapso API on a ticker and emits events for each new inbound
// message. It returns when ctx is cancelled.
func (p *Poller) Run(ctx context.Context, out chan<- delivery.Event) error {
	if err := os.MkdirAll(p.StateDir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	lastPoll := loadState(p.StateFile)
	if lastPoll.IsZero() {
		lastPoll = time.Now().UTC()
		saveState(p.StateFile, lastPoll)
		log.Printf("first run, starting from %s", lastPoll.Format(time.RFC3339))
	}

	// Poll immediately, then on interval.
	p.poll(&lastPoll, out)

	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.poll(&lastPoll, out)
		}
	}
}

func (p *Poller) poll(lastPoll *time.Time, out chan<- delivery.Event) {
	if p.pendingMedia == nil {
		p.pendingMedia = make(map[string]int)
	}

	since := lastPoll.Format(time.RFC3339)

	resp, err := p.Client.ListMessages(kapso.ListMessagesParams{
		Direction: "inbound",
		Since:     since,
		Limit:     100,
	})
	if err != nil {
		log.Printf("poll error: %v", err)
		return
	}

	if len(resp.Data) == 0 {
		return
	}

	var newest time.Time
	forwarded := 0
	deferred := 0

	for _, msg := range resp.Data {
		msgTime := parseTimestamp(msg.Timestamp)

		// Media messages without a URL: defer processing so the next poll
		// cycle can pick them up once Kapso finishes media processing.
		if delivery.IsMediaMessage(msg.Message) && !delivery.HasMediaURL(msg.Message) {
			retries := p.pendingMedia[msg.ID]
			if retries < maxMediaRetries {
				p.pendingMedia[msg.ID] = retries + 1
				if retries == 0 {
					log.Printf("media message %s has no URL yet, deferring (attempt %d/%d)", msg.ID, retries+1, maxMediaRetries)
				}
				deferred++
				// Don't advance cursor and don't emit — will retry next cycle.
				continue
			}
			// Max retries exhausted: forward as text-only and clean up.
			log.Printf("WARN: media message %s still has no URL after %d retries, forwarding as text-only", msg.ID, maxMediaRetries)
			delete(p.pendingMedia, msg.ID)
		} else {
			// Message now has URL (or is not media). Clean up pending tracker.
			if _, pending := p.pendingMedia[msg.ID]; pending {
				log.Printf("media message %s now has URL (after %d retries)", msg.ID, p.pendingMedia[msg.ID])
				delete(p.pendingMedia, msg.ID)
			}
		}

		// Advance cursor past this message.
		if !msgTime.IsZero() && msgTime.After(newest) {
			newest = msgTime
		}

		text, ok := delivery.ExtractText(msg.Message, p.Client, p.Transcriber, p.MaxAudioSize)
		if !ok {
			continue
		}

		images := delivery.ExtractImages(msg.Message, p.Client)

		name := ""
		if msg.Kapso != nil {
			name = msg.Kapso.ContactName
		}

		out <- delivery.Event{
			ID:     msg.ID,
			From:   msg.From,
			Name:   name,
			Text:   text,
			Images: images,
		}
		forwarded++
	}

	if forwarded > 0 {
		log.Printf("forwarded %d message(s)", forwarded)
	}
	if deferred > 0 {
		log.Printf("deferred %d media message(s) waiting for URL", deferred)
	}

	if !newest.IsZero() {
		*lastPoll = newest.Add(time.Second)
		saveState(p.StateFile, *lastPoll)
	}
}

func parseTimestamp(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
		return time.Unix(n, 0).UTC()
	}
	return time.Time{}
}

func loadState(path string) time.Time {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return t
}

func saveState(path string, t time.Time) {
	if err := os.WriteFile(path, []byte(t.Format(time.RFC3339)), 0o600); err != nil {
		log.Printf("WARN: failed to save poll state: %v", err)
	}
}
