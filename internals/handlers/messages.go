package handlers

import (
	"encoding/json"
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
		ChatID int    `json:"chat_id"`
		Text   string `json:"text"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Некорректный формат JSON", http.StatusBadRequest)
		return
	}

	if input.ChatID == 0 || input.Text == "" {
		http.Error(w, "Поля chat_id и text обязательны", http.StatusBadRequest)
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "success",
		"message_id": msgID,
		"created_at": createdAt,
	})
}

// GetMessagesHandler возвращает историю диалога между двумя юзерами
func GetMessagesHandler(w http.ResponseWriter, r *http.Request) {
	chatStr := r.URL.Query().Get("chat_id")
	chatID, _ := strconv.Atoi(chatStr)

	if chatID == 0 {
		http.Error(w, "Параметр chat_id обязателен и должен быть числом", http.StatusBadRequest)
		return
	}

	query := `
		SELECT id, sender_id, chat_id, text, created_at 
		FROM messages 
		WHERE chat_id = $1
		ORDER BY created_at ASC`

	rows, err := database.DB.Query(query, chatID)
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

	// Чтобы фронтенд не ловил null, если переписка еще пустая, отдаем пустой массив []
	if chatHistory == nil {
		chatHistory = []models.Message{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chatHistory)
}
