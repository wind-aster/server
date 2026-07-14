package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/middleware"
	"github.com/WindAster/server/internals/models"
	"github.com/WindAster/server/internals/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Wired from main() at startup, mirroring how database.DB is shared.
var (
	Store         storage.Storage
	MaxUploadSize int64
	MaxVideoSize  int64
)

// limitFor returns the max allowed byte size for a given mime type. Videos get
// their own (larger) cap; everything else uses the general upload limit.
func limitFor(mime string) int64 {
	if strings.HasPrefix(mime, "video/") {
		return MaxVideoSize
	}
	return MaxUploadSize
}

// isInlineType reports whether an object should be served inline (viewed in the
// browser) rather than as a download — images and videos.
func isInlineType(mime string) bool {
	return strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/")
}

// isChatMember reports whether userID belongs to chatID.
func isChatMember(chatID, userID int) bool {
	var one int
	err := database.DB.QueryRow(
		`SELECT 1 FROM chat_members WHERE chat_id = $1 AND user_id = $2`,
		chatID, userID,
	).Scan(&one)
	return err == nil
}

// CreateUploadHandler reserves an attachment row and returns presigned PUT URLs
// so the browser can upload directly to object storage.
func CreateUploadHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int)
	if !ok || userID == 0 {
		http.Error(w, "Ошибка идентификации пользователя", http.StatusUnauthorized)
		return
	}

	var in struct {
		ChatID    int    `json:"chat_id"`
		Filename  string `json:"filename"`
		MimeType  string `json:"mime_type"`
		Size      int64  `json:"size"`
		Width     *int   `json:"width"`
		Height    *int   `json:"height"`
		Thumbnail bool   `json:"thumbnail"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "Некорректный формат JSON", http.StatusBadRequest)
		return
	}
	if in.ChatID == 0 || in.Filename == "" {
		http.Error(w, "Поля chat_id и filename обязательны", http.StatusBadRequest)
		return
	}
	if in.MimeType == "" {
		in.MimeType = "application/octet-stream"
	}
	if in.Size > limitFor(in.MimeType) {
		http.Error(w, "Файл превышает максимальный размер", http.StatusRequestEntityTooLarge)
		return
	}
	if !isChatMember(in.ChatID, userID) {
		http.Error(w, "Нет доступа к этому чату", http.StatusForbidden)
		return
	}

	storageKey := uuid.NewString() + strings.ToLower(filepath.Ext(in.Filename))
	var thumbKey string
	if in.Thumbnail {
		thumbKey = "thumbs/" + uuid.NewString()
	}

	var attachmentID int
	err := database.DB.QueryRow(
		`INSERT INTO attachments
			(uploader_id, chat_id, storage_key, thumb_key, filename, mime_type, size_bytes, width, height, status)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9, 'pending')
		 RETURNING id`,
		userID, in.ChatID, storageKey, thumbKey, in.Filename, in.MimeType, in.Size, in.Width, in.Height,
	).Scan(&attachmentID)
	if err != nil {
		http.Error(w, "Ошибка создания вложения: "+err.Error(), http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	uploadURL, err := Store.PresignPut(ctx, storageKey)
	if err != nil {
		http.Error(w, "Ошибка подготовки загрузки: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"attachment_id": attachmentID,
		"upload_url":    uploadURL,
	}
	if thumbKey != "" {
		if thumbURL, err := Store.PresignPut(ctx, thumbKey); err == nil {
			resp["thumb_upload_url"] = thumbURL
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

// GetFileHandler returns fresh presigned URLs for a single attachment, used by
// the client to refresh links that have expired.
func GetFileHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int)
	if !ok || userID == 0 {
		http.Error(w, "Ошибка идентификации пользователя", http.StatusUnauthorized)
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id == 0 {
		http.Error(w, "Некорректный id вложения", http.StatusBadRequest)
		return
	}

	var (
		chatID     int
		filename   string
		mimeType   string
		sizeBytes  int64
		width      sql.NullInt64
		height     sql.NullInt64
		storageKey string
		thumbKey   sql.NullString
	)
	err = database.DB.QueryRow(
		`SELECT chat_id, filename, mime_type, size_bytes, width, height, storage_key, thumb_key
		 FROM attachments WHERE id = $1 AND status = 'ready'`, id,
	).Scan(&chatID, &filename, &mimeType, &sizeBytes, &width, &height, &storageKey, &thumbKey)
	if err == sql.ErrNoRows {
		http.Error(w, "Вложение не найдено", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "Ошибка получения вложения: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !isChatMember(chatID, userID) {
		http.Error(w, "Нет доступа к этому вложению", http.StatusForbidden)
		return
	}

	a := models.Attachment{ID: id, Filename: filename, MimeType: mimeType, SizeBytes: sizeBytes}
	if width.Valid {
		wv := int(width.Int64)
		a.Width = &wv
	}
	if height.Valid {
		hv := int(height.Int64)
		a.Height = &hv
	}
	ctx := r.Context()
	if u, err := Store.PresignGet(ctx, storageKey, filename, isInlineType(mimeType)); err == nil {
		a.URL = u
	}
	if thumbKey.Valid && thumbKey.String != "" {
		if u, err := Store.PresignGet(ctx, thumbKey.String, filename, true); err == nil {
			a.ThumbURL = u
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a)
}

// finalizeAttachments links pending attachments to a freshly created message.
// It only touches rows owned by senderID in chatID, and confirms each object
// actually exists in storage (guarding against a message that references an
// upload that never completed). Oversized uploads are rejected and deleted.
// Best-effort: failures are logged, not fatal to message delivery.
func finalizeAttachments(msgID, chatID, senderID int, ids []int) {
	if len(ids) == 0 {
		return
	}
	ctx := context.Background()
	for _, id := range ids {
		var storageKey, mimeType string
		var thumbKey string
		var tk sql.NullString
		err := database.DB.QueryRow(
			`SELECT storage_key, thumb_key, mime_type FROM attachments
			 WHERE id = $1 AND uploader_id = $2 AND chat_id = $3 AND status = 'pending'`,
			id, senderID, chatID,
		).Scan(&storageKey, &tk, &mimeType)
		if err != nil {
			continue // unknown / not owned / already finalized
		}
		thumbKey = tk.String

		size, _, err := Store.Stat(ctx, storageKey)
		if err != nil {
			log.Printf("finalize: object for attachment %d missing: %v", id, err)
			continue
		}
		if size > limitFor(mimeType) {
			log.Printf("finalize: attachment %d oversize (%d), rejecting", id, size)
			_ = Store.Delete(ctx, storageKey)
			if thumbKey != "" {
				_ = Store.Delete(ctx, thumbKey)
			}
			continue
		}

		if _, err := database.DB.Exec(
			`UPDATE attachments SET message_id = $1, status = 'ready', size_bytes = $2 WHERE id = $3`,
			msgID, size, id,
		); err != nil {
			log.Printf("finalize: failed to link attachment %d: %v", id, err)
		}
	}
}

// loadAttachmentsByMessage returns ready attachments for a single message, each
// enriched with fresh presigned URLs.
func loadAttachmentsByMessage(msgID int) []models.Attachment {
	m := loadAttachments("message_id = $1", msgID)
	return m[msgID]
}

// loadAttachmentsByIDs returns ready attachments for the given message ids,
// keyed by message id. Used by the paginated message handler so enrichment is
// scoped to a single page rather than the whole chat.
func loadAttachmentsByIDs(ids []int) map[int][]models.Attachment {
	if len(ids) == 0 {
		return map[int][]models.Attachment{}
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	where := "message_id IN (" + strings.Join(placeholders, ",") + ")"
	return loadAttachments(where, args...)
}

// loadAttachments runs the shared query (filtered by the given WHERE fragment)
// and builds a message_id -> attachments map with presigned URLs.
func loadAttachments(where string, args ...interface{}) map[int][]models.Attachment {
	result := make(map[int][]models.Attachment)
	if Store == nil {
		return result
	}
	rows, err := database.DB.Query(
		`SELECT id, message_id, filename, mime_type, size_bytes, width, height, storage_key, thumb_key
		 FROM attachments
		 WHERE `+where+` AND status = 'ready' AND message_id IS NOT NULL
		 ORDER BY id ASC`, args...)
	if err != nil {
		log.Printf("loadAttachments: query failed: %v", err)
		return result
	}
	defer rows.Close()

	ctx := context.Background()
	for rows.Next() {
		var (
			a          models.Attachment
			msgID      int
			width      sql.NullInt64
			height     sql.NullInt64
			storageKey string
			thumbKey   sql.NullString
		)
		if err := rows.Scan(&a.ID, &msgID, &a.Filename, &a.MimeType, &a.SizeBytes,
			&width, &height, &storageKey, &thumbKey); err != nil {
			log.Printf("loadAttachments: scan failed: %v", err)
			continue
		}
		if width.Valid {
			wv := int(width.Int64)
			a.Width = &wv
		}
		if height.Valid {
			hv := int(height.Int64)
			a.Height = &hv
		}
		if u, err := Store.PresignGet(ctx, storageKey, a.Filename, isInlineType(a.MimeType)); err == nil {
			a.URL = u
		}
		if thumbKey.Valid && thumbKey.String != "" {
			if u, err := Store.PresignGet(ctx, thumbKey.String, a.Filename, true); err == nil {
				a.ThumbURL = u
			}
		}
		result[msgID] = append(result[msgID], a)
	}
	return result
}
