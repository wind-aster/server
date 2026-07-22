package models

import "time"

type Message struct {
	ID           int           `json:"id"`
	SenderID     int           `json:"sender_id"`
	ChatID       int           `json:"chat_id"`
	Text         string        `json:"text"`
	CreatedAt    time.Time     `json:"created_at"`
	Attachments  []Attachment  `json:"attachments,omitempty"`
	ReplyToID    *int          `json:"reply_to_id,omitempty"`
	EditedAt     *time.Time    `json:"edited_at,omitempty"`
	ReplyPreview *ReplyPreview `json:"reply_preview,omitempty"`
}

// ReplyPreview is a lightweight quote of the message a reply points at, so the
// client can render the quote without a second fetch. Text is a snippet (or a
// placeholder if the target was deleted).
type ReplyPreview struct {
	ID         int    `json:"id"`
	SenderName string `json:"sender_name"`
	Text       string `json:"text"`
}

// Attachment is a file linked to a message. URL/ThumbURL are short-lived
// presigned GET links generated per response; they are never persisted.
type Attachment struct {
	ID        int    `json:"id"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	Width     *int   `json:"width,omitempty"`
	Height    *int   `json:"height,omitempty"`
	URL       string `json:"url"`
	ThumbURL  string `json:"thumb_url,omitempty"`
}
