package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/middleware"
	"github.com/WindAster/server/internals/models"
	"github.com/go-chi/chi/v5"
)

// allowedReactions is the fixed quick-reaction set. Kept byte-for-byte in sync
// with QUICK_REACTIONS on the frontend (note ❤️ carries a variation selector).
var allowedReactions = map[string]bool{
	"👍":  true,
	"👎":  true,
	"❤️": true,
	"😂":  true,
	"😮":  true,
	"😢":  true,
}

// loadReactionsByIDs returns reactions for the given message ids, keyed by
// message id, aggregated per emoji in first-seen order (mirrors
// loadAttachmentsByIDs; aggregating in Go avoids Postgres array scanning).
func loadReactionsByIDs(ids []int) map[int][]models.Reaction {
	result := make(map[int][]models.Reaction)
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
		`SELECT message_id, emoji, user_id FROM message_reactions
		 WHERE message_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY message_id, id`, args...)
	if err != nil {
		return result
	}
	defer rows.Close()

	// Track the index of each emoji within a message so counts/user lists
	// accumulate while preserving first-seen order.
	idx := make(map[int]map[string]int)
	for rows.Next() {
		var (
			msgID  int
			emoji  string
			userID int
		)
		if err := rows.Scan(&msgID, &emoji, &userID); err != nil {
			continue
		}
		if idx[msgID] == nil {
			idx[msgID] = make(map[string]int)
		}
		if pos, ok := idx[msgID][emoji]; ok {
			result[msgID][pos].Count++
			result[msgID][pos].UserIDs = append(result[msgID][pos].UserIDs, userID)
		} else {
			idx[msgID][emoji] = len(result[msgID])
			result[msgID] = append(result[msgID], models.Reaction{
				Emoji:   emoji,
				Count:   1,
				UserIDs: []int{userID},
			})
		}
	}
	return result
}

// loadReactionsByMessage returns the aggregated reactions for a single message.
func loadReactionsByMessage(id int) []models.Reaction {
	return loadReactionsByIDs([]int{id})[id]
}

// ToggleReactionHandler adds the caller's reaction to a message, or removes it
// if already present, then broadcasts the message's new reaction state.
func ToggleReactionHandler(w http.ResponseWriter, r *http.Request) {
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
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Некорректный формат JSON", http.StatusBadRequest)
		return
	}
	if !allowedReactions[input.Emoji] {
		http.Error(w, "Недопустимая реакция", http.StatusBadRequest)
		return
	}

	var (
		chatID   int
		senderID int
	)
	if err := database.DB.QueryRow(
		`SELECT chat_id, sender_id FROM messages WHERE id = $1 AND deleted_at IS NULL`, id,
	).Scan(&chatID, &senderID); err != nil {
		http.Error(w, "Сообщение не найдено", http.StatusNotFound)
		return
	}
	if !isChatMember(chatID, userID) {
		http.Error(w, "Нет доступа к этому чату", http.StatusForbidden)
		return
	}

	// Toggle: remove the reaction if it exists, otherwise add it.
	res, err := database.DB.Exec(
		`DELETE FROM message_reactions WHERE message_id = $1 AND user_id = $2 AND emoji = $3`,
		id, userID, input.Emoji,
	)
	if err != nil {
		http.Error(w, "Ошибка обновления реакции: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// added is true when we inserted (nothing was deleted) — used by the client
	// to decide whether to notify the message author.
	added := false
	if n, _ := res.RowsAffected(); n == 0 {
		added = true
		if _, err := database.DB.Exec(
			`INSERT INTO message_reactions (message_id, user_id, emoji) VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`,
			id, userID, input.Emoji,
		); err != nil {
			http.Error(w, "Ошибка добавления реакции: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	reactions := loadReactionsByMessage(id)
	broadcastEvent(chatID, "message_reaction", map[string]interface{}{
		"chat_id":    chatID,
		"message_id": id,
		"reactions":  reactions,
		"sender_id":  senderID,
		"actor_id":   userID,
		"added":      added,
		"emoji":      input.Emoji,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"reactions": reactions})
}
