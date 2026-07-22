package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/middleware"
	"github.com/WindAster/server/internals/models"
	"github.com/go-chi/chi/v5"
)

// SendMessageHandler сохраняет новое сообщение в базу
func SendMessageHandler(w http.ResponseWriter, r *http.Request) {
	senderID, ok := r.Context().Value(middleware.UserIDKey).(int)
	if !ok || senderID == 0 {
		http.Error(w, "Ошибка идентификации пользователя", http.StatusUnauthorized)
		return
	}

	var input struct {
		ChatID        int    `json:"chat_id"`
		Text          string `json:"text"`
		AttachmentIDs []int  `json:"attachment_ids"`
		ReplyToID     *int   `json:"reply_to_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Некорректный формат JSON", http.StatusBadRequest)
		return
	}

	// Text may be empty when the message carries at least one attachment.
	if input.ChatID == 0 || (input.Text == "" && len(input.AttachmentIDs) == 0) {
		http.Error(w, "Требуется chat_id и текст либо вложение", http.StatusBadRequest)
		return
	}

	msg, err := createMessage(senderID, input.ChatID, input.Text, input.AttachmentIDs, input.ReplyToID)
	if err != nil {
		http.Error(w, "Ошибка при сохранении сообщения в базу: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "success",
		"message_id":    msg.ID,
		"created_at":    msg.CreatedAt,
		"attachments":   msg.Attachments,
		"reply_preview": msg.ReplyPreview,
	})
}

// GetMessagesHandler возвращает историю диалога между двумя юзерами
func GetMessagesHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int)
	if !ok || userID == 0 {
		http.Error(w, "Ошибка идентификации пользователя", http.StatusUnauthorized)
		return
	}

	chatStr := r.URL.Query().Get("chat_id")
	chatID, _ := strconv.Atoi(chatStr)

	if chatID == 0 {
		http.Error(w, "Параметр chat_id обязателен и должен быть числом", http.StatusBadRequest)
		return
	}

	if !isChatMember(chatID, userID) {
		http.Error(w, "Нет доступа к этому чату", http.StatusForbidden)
		return
	}

	// Keyset pagination: newest `limit` messages with id < `before` (cursor).
	// Ordering by id (SERIAL, monotonic) avoids created_at ties.
	limit := 50
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		if l > 100 {
			l = 100
		}
		limit = l
	}
	before := int64(math.MaxInt64)
	if b, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil && b > 0 {
		before = b
	}

	rows, err := database.DB.Query(`
		SELECT id, sender_id, chat_id, text, created_at, reply_to_id, edited_at
		FROM messages
		WHERE chat_id = $1 AND id < $2 AND deleted_at IS NULL
		ORDER BY id DESC
		LIMIT $3`, chatID, before, limit)
	if err != nil {
		http.Error(w, "Ошибка получения сообщений из базы: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var chatHistory []models.Message

	for rows.Next() {
		var (
			msg      models.Message
			replyTo  sql.NullInt64
			editedAt sql.NullTime
		)
		if err := rows.Scan(&msg.ID, &msg.SenderID, &msg.ChatID, &msg.Text, &msg.CreatedAt, &replyTo, &editedAt); err != nil {
			http.Error(w, "Ошибка обработки данных: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if replyTo.Valid {
			v := int(replyTo.Int64)
			msg.ReplyToID = &v
		}
		if editedAt.Valid {
			t := editedAt.Time
			msg.EditedAt = &t
		}
		chatHistory = append(chatHistory, msg)
	}

	// Query returns newest-first; reverse to ascending for display.
	for i, j := 0, len(chatHistory)-1; i < j; i, j = i+1, j-1 {
		chatHistory[i], chatHistory[j] = chatHistory[j], chatHistory[i]
	}

	// Чтобы фронтенд не ловил null, если переписка еще пустая, отдаем пустой массив []
	if chatHistory == nil {
		chatHistory = []models.Message{}
	}

	// Enrich only this page's messages with attachments + presigned URLs.
	ids := make([]int, len(chatHistory))
	replyIDs := make([]int, 0, len(chatHistory))
	for i := range chatHistory {
		ids[i] = chatHistory[i].ID
		if chatHistory[i].ReplyToID != nil {
			replyIDs = append(replyIDs, *chatHistory[i].ReplyToID)
		}
	}
	byMsg := loadAttachmentsByIDs(ids)
	previews := loadReplyPreviewsByIDs(replyIDs)
	reactions := loadReactionsByIDs(ids)
	for i := range chatHistory {
		if att := byMsg[chatHistory[i].ID]; att != nil {
			chatHistory[i].Attachments = att
		}
		if chatHistory[i].ReplyToID != nil {
			chatHistory[i].ReplyPreview = previews[*chatHistory[i].ReplyToID]
		}
		if rx := reactions[chatHistory[i].ID]; rx != nil {
			chatHistory[i].Reactions = rx
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chatHistory)
}

// createMessage persists a new message (with an optional reply target and
// attachments), broadcasts a "message" event to the chat, and returns the
// fully-built model. Shared by the REST and WebSocket send paths.
func createMessage(senderID, chatID int, text string, attachmentIDs []int, replyToID *int) (models.Message, error) {
	// Only keep reply_to_id if the target exists, is not deleted, and is in the
	// same chat — otherwise drop it silently.
	if replyToID != nil {
		var targetChat int
		err := database.DB.QueryRow(
			`SELECT chat_id FROM messages WHERE id = $1 AND deleted_at IS NULL`, *replyToID,
		).Scan(&targetChat)
		if err != nil || targetChat != chatID {
			replyToID = nil
		}
	}

	var msg models.Message
	if err := database.DB.QueryRow(
		`INSERT INTO messages (sender_id, chat_id, text, reply_to_id)
		 VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		senderID, chatID, text, replyToID,
	).Scan(&msg.ID, &msg.CreatedAt); err != nil {
		return models.Message{}, err
	}
	msg.SenderID = senderID
	msg.ChatID = chatID
	msg.Text = text
	msg.ReplyToID = replyToID

	// Link uploaded attachments to this message before broadcasting.
	finalizeAttachments(msg.ID, chatID, senderID, attachmentIDs)
	msg.Attachments = loadAttachmentsByMessage(msg.ID)
	if replyToID != nil {
		msg.ReplyPreview = loadReplyPreview(*replyToID)
	}

	broadcastEvent(chatID, "message", map[string]interface{}{"message": msg})
	// Recipients with a live socket count as delivered immediately.
	markDeliveredToOnline(chatID, msg.ID, senderID)
	return msg, nil
}

// loadMessageByID rebuilds a single message with its attachments + reply preview
// (used to broadcast the authoritative object after an edit).
func loadMessageByID(id int) (models.Message, error) {
	var (
		msg      models.Message
		replyTo  sql.NullInt64
		editedAt sql.NullTime
	)
	err := database.DB.QueryRow(
		`SELECT id, sender_id, chat_id, text, created_at, reply_to_id, edited_at
		 FROM messages WHERE id = $1`, id,
	).Scan(&msg.ID, &msg.SenderID, &msg.ChatID, &msg.Text, &msg.CreatedAt, &replyTo, &editedAt)
	if err != nil {
		return models.Message{}, err
	}
	if replyTo.Valid {
		v := int(replyTo.Int64)
		msg.ReplyToID = &v
		msg.ReplyPreview = loadReplyPreview(v)
	}
	if editedAt.Valid {
		t := editedAt.Time
		msg.EditedAt = &t
	}
	msg.Attachments = loadAttachmentsByMessage(id)
	msg.Reactions = loadReactionsByMessage(id)
	return msg, nil
}

// EditMessageHandler updates the text of the caller's own message.
func EditMessageHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int)
	if !ok || userID == 0 {
		http.Error(w, "Ошибка идентификации пользователя", http.StatusUnauthorized)
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id == 0 {
		http.Error(w, "Некорректный id сообщения", http.StatusBadRequest)
		return
	}

	var input struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Некорректный формат JSON", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(input.Text)
	if text == "" {
		http.Error(w, "Текст не может быть пустым", http.StatusBadRequest)
		return
	}

	var (
		senderID  int
		deletedAt sql.NullTime
	)
	if err := database.DB.QueryRow(
		`SELECT sender_id, deleted_at FROM messages WHERE id = $1`, id,
	).Scan(&senderID, &deletedAt); err != nil {
		http.Error(w, "Сообщение не найдено", http.StatusNotFound)
		return
	}
	// Can't edit someone else's message or a system message (sender 0).
	if senderID != userID || senderID == 0 {
		http.Error(w, "Нет прав на редактирование этого сообщения", http.StatusForbidden)
		return
	}
	if deletedAt.Valid {
		http.Error(w, "Сообщение удалено", http.StatusBadRequest)
		return
	}

	if _, err := database.DB.Exec(
		`UPDATE messages SET text = $1, edited_at = now() WHERE id = $2`, text, id,
	); err != nil {
		http.Error(w, "Ошибка обновления сообщения: "+err.Error(), http.StatusInternalServerError)
		return
	}

	msg, err := loadMessageByID(id)
	if err != nil {
		http.Error(w, "Ошибка загрузки сообщения: "+err.Error(), http.StatusInternalServerError)
		return
	}

	broadcastEvent(msg.ChatID, "message_updated", map[string]interface{}{"message": msg})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msg)
}

// DeleteMessageHandler soft-deletes the caller's own message (so replies keep
// their referential target) and purges its attachment objects from storage.
func DeleteMessageHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int)
	if !ok || userID == 0 {
		http.Error(w, "Ошибка идентификации пользователя", http.StatusUnauthorized)
		return
	}
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id == 0 {
		http.Error(w, "Некорректный id сообщения", http.StatusBadRequest)
		return
	}

	var (
		senderID int
		chatID   int
	)
	if err := database.DB.QueryRow(
		`SELECT sender_id, chat_id FROM messages WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&senderID, &chatID); err != nil {
		http.Error(w, "Сообщение не найдено", http.StatusNotFound)
		return
	}
	if senderID != userID {
		http.Error(w, "Нет прав на удаление этого сообщения", http.StatusForbidden)
		return
	}

	purgeMessageAttachments(id)

	if _, err := database.DB.Exec(
		`UPDATE messages SET deleted_at = now() WHERE id = $1`, id,
	); err != nil {
		http.Error(w, "Ошибка удаления сообщения: "+err.Error(), http.StatusInternalServerError)
		return
	}

	broadcastEvent(chatID, "message_deleted", map[string]interface{}{
		"chat_id":    chatID,
		"message_id": id,
	})

	w.WriteHeader(http.StatusNoContent)
}

// purgeMessageAttachments removes a message's attachment objects from storage
// and deletes their rows. Best-effort — storage errors are ignored.
func purgeMessageAttachments(msgID int) {
	rows, err := database.DB.Query(
		`SELECT storage_key, thumb_key FROM attachments WHERE message_id = $1`, msgID)
	if err != nil {
		return
	}
	type obj struct {
		key   string
		thumb sql.NullString
	}
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.key, &o.thumb); err == nil {
			objs = append(objs, o)
		}
	}
	rows.Close()

	ctx := context.Background()
	for _, o := range objs {
		if Store != nil {
			_ = Store.Delete(ctx, o.key)
			if o.thumb.Valid && o.thumb.String != "" {
				_ = Store.Delete(ctx, o.thumb.String)
			}
		}
	}
	_, _ = database.DB.Exec(`DELETE FROM attachments WHERE message_id = $1`, msgID)
}

// replyPreviewText normalizes a target message's body into a short quote label.
func replyPreviewText(text string, deleted bool) string {
	if deleted {
		return "Deleted message"
	}
	if strings.TrimSpace(text) == "" {
		return "Attachment"
	}
	return text
}

// loadReplyPreview builds a single reply quote for the given target message id.
func loadReplyPreview(id int) *models.ReplyPreview {
	var (
		text      string
		senderID  int
		deletedAt sql.NullTime
	)
	if err := database.DB.QueryRow(
		`SELECT text, sender_id, deleted_at FROM messages WHERE id = $1`, id,
	).Scan(&text, &senderID, &deletedAt); err != nil {
		return nil
	}
	rp := &models.ReplyPreview{ID: id, Text: replyPreviewText(text, deletedAt.Valid)}
	if !deletedAt.Valid {
		rp.SenderName = senderDisplayName(senderID)
	}
	return rp
}

// loadReplyPreviewsByIDs batch-loads reply quotes for a set of target ids,
// keyed by target message id (mirrors loadAttachmentsByIDs).
func loadReplyPreviewsByIDs(ids []int) map[int]*models.ReplyPreview {
	result := make(map[int]*models.ReplyPreview)
	if len(ids) == 0 {
		return result
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := database.DB.Query(
		`SELECT m.id, m.text, m.deleted_at, COALESCE(NULLIF(u.display_name, ''), u.username, '')
		 FROM messages m
		 LEFT JOIN users u ON u.id = m.sender_id
		 WHERE m.id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id        int
			text      string
			deletedAt sql.NullTime
			name      string
		)
		if err := rows.Scan(&id, &text, &deletedAt, &name); err != nil {
			continue
		}
		rp := &models.ReplyPreview{ID: id, Text: replyPreviewText(text, deletedAt.Valid)}
		if !deletedAt.Valid {
			rp.SenderName = name
		}
		result[id] = rp
	}
	return result
}

// senderDisplayName resolves a user's display name (falling back to username).
func senderDisplayName(userID int) string {
	var name string
	if err := database.DB.QueryRow(
		`SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = $1`, userID,
	).Scan(&name); err != nil {
		return ""
	}
	return name
}
