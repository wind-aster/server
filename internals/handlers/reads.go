package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/middleware"
	"github.com/go-chi/chi/v5"
)

// broadcastReadStatus emits a member's current read/delivered pointers to the
// chat so senders can update delivery/read receipts.
func broadcastReadStatus(chatID, userID int) {
	var lastRead, lastDelivered int
	if err := database.DB.QueryRow(
		`SELECT last_read_message_id, last_delivered_message_id
		 FROM chat_members WHERE chat_id = $1 AND user_id = $2`,
		chatID, userID,
	).Scan(&lastRead, &lastDelivered); err != nil {
		return
	}
	broadcastEvent(chatID, "read_status", map[string]interface{}{
		"chat_id":        chatID,
		"user_id":        userID,
		"last_read":      lastRead,
		"last_delivered": lastDelivered,
	})
}

// markDelivered advances a member's delivered pointer (only forward) and, if it
// changed, broadcasts the new status.
func markDelivered(chatID, userID, upto int) {
	res, err := database.DB.Exec(
		`UPDATE chat_members SET last_delivered_message_id = $1
		 WHERE chat_id = $2 AND user_id = $3 AND last_delivered_message_id < $1`,
		upto, chatID, userID,
	)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		broadcastReadStatus(chatID, userID)
	}
}

// markReadDelivered advances both the read and delivered pointers (read implies
// delivered) and broadcasts the new status.
func markReadDelivered(chatID, userID, upto int) {
	res, err := database.DB.Exec(
		`UPDATE chat_members
		 SET last_read_message_id = GREATEST(last_read_message_id, $1),
		     last_delivered_message_id = GREATEST(last_delivered_message_id, $1)
		 WHERE chat_id = $2 AND user_id = $3
		   AND (last_read_message_id < $1 OR last_delivered_message_id < $1)`,
		upto, chatID, userID,
	)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		broadcastReadStatus(chatID, userID)
	}
}

// markDeliveredAllChats marks a user delivered up to the latest message in every
// chat they belong to (used on WS connect to cover offline-at-send recipients).
func markDeliveredAllChats(userID int) {
	rows, err := database.DB.Query(
		`UPDATE chat_members cm
		 SET last_delivered_message_id = GREATEST(
		     cm.last_delivered_message_id,
		     COALESCE((SELECT MAX(id) FROM messages m WHERE m.chat_id = cm.chat_id), 0))
		 WHERE cm.user_id = $1
		   AND cm.last_delivered_message_id < COALESCE(
		       (SELECT MAX(id) FROM messages m WHERE m.chat_id = cm.chat_id), 0)
		 RETURNING cm.chat_id`,
		userID,
	)
	if err != nil {
		return
	}
	var chatIDs []int
	for rows.Next() {
		var cid int
		if err := rows.Scan(&cid); err == nil {
			chatIDs = append(chatIDs, cid)
		}
	}
	rows.Close()
	for _, cid := range chatIDs {
		broadcastReadStatus(cid, userID)
	}
}

// ReadChatHandler marks the caller read up to a message in a chat they belong to.
func ReadChatHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int)
	if !ok || userID == 0 {
		http.Error(w, "Ошибка идентификации пользователя", http.StatusUnauthorized)
		return
	}
	chatID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || chatID == 0 {
		http.Error(w, "Некорректный id чата", http.StatusBadRequest)
		return
	}

	var input struct {
		MessageID int `json:"message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.MessageID == 0 {
		http.Error(w, "Требуется message_id", http.StatusBadRequest)
		return
	}

	if !isChatMember(chatID, userID) {
		http.Error(w, "Нет доступа к этому чату", http.StatusForbidden)
		return
	}

	markReadDelivered(chatID, userID, input.MessageID)
	w.WriteHeader(http.StatusNoContent)
}
