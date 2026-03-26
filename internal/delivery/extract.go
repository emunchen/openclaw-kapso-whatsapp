package delivery

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Enriquefft/openclaw-kapso-whatsapp/internal/kapso"
	"github.com/Enriquefft/openclaw-kapso-whatsapp/internal/transcribe"
)

// ExtractText converts an inbound message of any supported type into a text
// representation suitable for forwarding to the gateway. It returns the text
// and true on success, or ("", false) if the message should be skipped.
// Unsupported types are logged and trigger a WhatsApp reply to the sender.
//
// Audio transcription priority:
//  1. Server-side transcript from Kapso (msg.Kapso.Transcript.Text)
//  2. Local transcription via tr (download from msg.Kapso.MediaURL)
//  3. Fallback to "[audio] (mime)" format
func ExtractText(msg kapso.Message, client *kapso.Client, tr transcribe.Transcriber, maxAudioSize int64) (string, bool) {
	switch msg.Type {
	case "text":
		if msg.Text == nil {
			return "", false
		}
		return msg.Text.Body, true

	case "image":
		if msg.Image == nil {
			return "", false
		}
		return formatMediaMessage("image", msg.Image.Caption, msg.Image.MimeType, msg.Kapso), true

	case "document":
		if msg.Document == nil {
			return "", false
		}
		label := msg.Document.Filename
		if label == "" {
			label = msg.Document.Caption
		}

		// Try PDF text extraction.
		if client != nil && isPDF(msg.Document.MimeType) {
			if mediaURL := kapsoMediaURL(msg.Kapso); mediaURL != "" {
				if text, err := extractPDFText(mediaURL, client); err == nil {
					prefix := "[document]"
					if label != "" {
						prefix += " " + label
					}
					log.Printf("extracted PDF text for message %s (%d chars)", msg.ID, len(text))
					return prefix + "\n" + text, true
				} else {
					log.Printf("WARN: PDF text extraction failed for message %s: %v", msg.ID, err)
				}
			}
		}

		// Fallback for non-PDF or extraction failure.
		return formatMediaMessage("document", label, msg.Document.MimeType, msg.Kapso), true

	case "audio":
		if msg.Audio == nil {
			return "", false
		}
		// 1. Use server-side transcript from Kapso if available.
		if msg.Kapso != nil && msg.Kapso.Transcript != nil && msg.Kapso.Transcript.Text != "" {
			return "[voice] " + msg.Kapso.Transcript.Text, true
		}
		// 2. Local transcription via configured transcriber.
		if tr != nil {
			mediaURL := kapsoMediaURL(msg.Kapso)
			if mediaURL != "" {
				if audio, err := client.DownloadMedia(mediaURL, maxAudioSize); err == nil {
					if text, err := tr.Transcribe(context.Background(), audio, msg.Audio.MimeType); err == nil {
						return "[voice] " + text, true
					} else {
						log.Printf("WARN: transcription failed for message %s: %v", msg.ID, err)
					}
				} else {
					log.Printf("WARN: audio download failed for message %s: %v", msg.ID, err)
				}
			} else {
				log.Printf("WARN: no media URL available for audio message %s", msg.ID)
			}
		}
		// 3. Fallback.
		return formatMediaMessage("audio", "", msg.Audio.MimeType, msg.Kapso), true

	case "video":
		if msg.Video == nil {
			return "", false
		}
		return formatMediaMessage("video", msg.Video.Caption, msg.Video.MimeType, msg.Kapso), true

	case "location":
		if msg.Location == nil {
			return "", false
		}
		return formatLocationMessage(msg.Location), true

	default:
		log.Printf("unsupported message type %q from %s (id=%s)", msg.Type, msg.From, msg.ID)
		go notifyUnsupported(msg.From, msg.Type, client)
		return "", false
	}
}

// kapsoMediaURL returns the media URL from KapsoMeta, falling back to
// MediaData.URL if the top-level MediaURL is empty.
func kapsoMediaURL(k *kapso.KapsoMeta) string {
	if k == nil {
		return ""
	}
	if k.MediaURL != "" {
		return k.MediaURL
	}
	if k.MediaData != nil && k.MediaData.URL != "" {
		return k.MediaData.URL
	}
	return ""
}

// formatMediaMessage builds a text representation for a media attachment.
// It uses the media URL from Kapso enrichment when available.
func formatMediaMessage(kind, label, mimeType string, k *kapso.KapsoMeta) string {
	var parts []string
	parts = append(parts, "["+kind+"]")
	if label != "" {
		parts = append(parts, label)
	}
	if mimeType != "" {
		parts = append(parts, "("+mimeType+")")
	}

	if url := kapsoMediaURL(k); url != "" {
		parts = append(parts, url)
	}

	return strings.Join(parts, " ")
}

// formatLocationMessage builds a text representation for a location message.
func formatLocationMessage(loc *kapso.LocationContent) string {
	var parts []string
	parts = append(parts, "[location]")
	if loc.Name != "" {
		parts = append(parts, loc.Name)
	}
	if loc.Address != "" {
		parts = append(parts, loc.Address)
	}
	parts = append(parts, fmt.Sprintf("(%.6f, %.6f)", loc.Latitude, loc.Longitude))
	return strings.Join(parts, " ")
}

// maxDocumentSize is the maximum document download size (20 MB).
const maxDocumentSize int64 = 20 * 1024 * 1024

// isPDF returns true if the MIME type indicates a PDF document.
func isPDF(mimeType string) bool {
	return strings.Contains(strings.ToLower(mimeType), "pdf")
}

// extractPDFText downloads a PDF from mediaURL and extracts text using pdftotext.
func extractPDFText(mediaURL string, client *kapso.Client) (string, error) {
	data, err := client.DownloadMedia(mediaURL, maxDocumentSize)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}

	tmpFile, err := os.CreateTemp("", "kapso-pdf-*.pdf")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return "", fmt.Errorf("write temp file: %w", err)
	}
	tmpFile.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "pdftotext", "-layout", tmpPath, "-").Output()
	if err != nil {
		return "", fmt.Errorf("pdftotext: %w", err)
	}

	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("pdftotext produced empty output (scanned PDF?)")
	}

	// Truncate very large extractions to avoid flooding the gateway.
	const maxTextLen = 50000
	if len(text) > maxTextLen {
		text = text[:maxTextLen] + "\n[... texto truncado]"
	}

	return text, nil
}

// maxImageSize is the default maximum image download size (10 MB).
const maxImageSize int64 = 10 * 1024 * 1024

// ExtractImages downloads image data from an image message. Returns nil for
// non-image messages or when the download fails (the text fallback in
// ExtractText still provides context). The client is required for
// authenticated media downloads from Kapso.
func ExtractImages(msg kapso.Message, client *kapso.Client) []ImageAttachment {
	if msg.Type != "image" || msg.Image == nil || client == nil {
		return nil
	}

	mediaURL := kapsoMediaURL(msg.Kapso)
	if mediaURL == "" {
		log.Printf("WARN: image message %s has no media URL (kapso==nil:%v)", msg.ID, msg.Kapso == nil)
		return nil
	}

	data, err := client.DownloadMedia(mediaURL, maxImageSize)
	if err != nil {
		log.Printf("WARN: image download failed for message %s: %v", msg.ID, err)
		return nil
	}

	log.Printf("downloaded image for message %s (%d bytes, %s)", msg.ID, len(data), msg.Image.MimeType)

	mimeType := msg.Image.MimeType
	if mimeType == "" {
		mimeType = "image/jpeg"
	}

	return []ImageAttachment{{
		Data:     data,
		MimeType: mimeType,
	}}
}

// notifyUnsupported sends a WhatsApp reply informing the user that their
// message type is not yet supported.
func notifyUnsupported(from, msgType string, client *kapso.Client) {
	to := from
	if !strings.HasPrefix(to, "+") {
		to = "+" + to
	}
	reply := fmt.Sprintf("Sorry, I can't process %s messages yet. Please send text instead.", msgType)
	if _, err := client.SendText(to, reply); err != nil {
		log.Printf("failed to send unsupported-type notice to %s: %v", to, err)
	}
}
