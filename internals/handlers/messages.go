package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"

	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/middleware"
	"github.com/WindAster/server/internals/models"
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

	query := `INSERT INTO messages (sender_id, chat_id, text) VALUES ($1, $2, $3) RETURNING id, created_at`

	var msgID int
	var createdAt string

	err := database.DB.QueryRow(query, senderID, input.ChatID, input.Text).Scan(&msgID, &createdAt)
	if err != nil {
		http.Error(w, "Ошибка при сохранении сообщения в базу: "+err.Error(), http.StatusInternalServerError)
		return
	}

	finalizeAttachments(msgID, input.ChatID, senderID, input.AttachmentIDs)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "success",
		"message_id":  msgID,
		"created_at":  createdAt,
		"attachments": loadAttachmentsByMessage(msgID),
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
		SELECT id, sender_id, chat_id, text, created_at
		FROM messages
		WHERE chat_id = $1 AND id < $2
		ORDER BY id DESC
		LIMIT $3`, chatID, before, limit)
	if err != nil {
		http.Error(w, "Ошибка получения сообщений из базы: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var chatHistory []models.Message

	for rows.Next() {
		var msg models.Message
		if err := rows.Scan(&msg.ID, &msg.SenderID, &msg.ChatID, &msg.Text, &msg.CreatedAt); err != nil {
			http.Error(w, "Ошибка обработки данных: "+err.Error(), http.StatusInternalServerError)
			return
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
	for i := range chatHistory {
		ids[i] = chatHistory[i].ID
	}
	byMsg := loadAttachmentsByIDs(ids)
	for i := range chatHistory {
		if att := byMsg[chatHistory[i].ID]; att != nil {
			chatHistory[i].Attachments = att
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chatHistory)
}
