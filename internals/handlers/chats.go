package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/middleware"
	"github.com/WindAster/server/internals/models"
)

func GetChatsHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(middleware.UserIDKey).(int)
	if !ok || userID == 0 {
		http.Error(w, "Ошибка идентификации пользователя", http.StatusUnauthorized)
		return
	}

	rows, err := database.DB.Query(`
		SELECT c.id, c.title, c.type
		FROM chats c
		JOIN chat_members cm ON cm.chat_id = c.id
		WHERE cm.user_id = $1`, userID)
	if err != nil {
		http.Error(w, "Ошибка получения чатов: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var chats []models.ChatDetail
	for rows.Next() {
		var c models.ChatDetail
		if err := rows.Scan(&c.ID, &c.Title, &c.Type); err != nil {
			http.Error(w, "Ошибка чтения данных чата: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Fetch members (+ presence: online from live sockets, last_seen from DB).
		memberRows, err := database.DB.Query(`
			SELECT u.id, u.username, u.display_name, u.email, u.last_seen_at
			FROM users u
			JOIN chat_members cm ON cm.user_id = u.id
			WHERE cm.chat_id = $1`, c.ID)
		if err != nil {
			http.Error(w, "Ошибка получения участников чата: "+err.Error(), http.StatusInternalServerError)
			return
		}
		for memberRows.Next() {
			var u models.User
			var lastSeen sql.NullTime
			if err := memberRows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &lastSeen); err != nil {
				memberRows.Close()
				http.Error(w, "Ошибка чтения участника: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if lastSeen.Valid {
				t := lastSeen.Time
				u.LastSeen = &t
			}
			u.Online = isOnline(u.ID)
			c.Members = append(c.Members, u)
		}
		memberRows.Close()

		// Fetch last message
		var lastMsg models.Message
		err = database.DB.QueryRow(`
			SELECT id, sender_id, chat_id, text, created_at
			FROM messages
			WHERE chat_id = $1 AND deleted_at IS NULL
			ORDER BY created_at DESC
			LIMIT 1`, c.ID).Scan(&lastMsg.ID, &lastMsg.SenderID, &lastMsg.ChatID, &lastMsg.Text, &lastMsg.CreatedAt)
		if err == nil {
			c.LastMessages = []models.Message{lastMsg}
		} else {
			c.LastMessages = []models.Message{}
		}

		if c.Members == nil {
			c.Members = []models.User{}
		}

		// Read/delivered pointers for every member (drives receipts).
		statusRows, err := database.DB.Query(`
			SELECT user_id, last_read_message_id, last_delivered_message_id
			FROM chat_members WHERE chat_id = $1`, c.ID)
		if err != nil {
			http.Error(w, "Ошибка получения статусов чата: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var myLastRead int
		for statusRows.Next() {
			var ms models.MemberStatus
			if err := statusRows.Scan(&ms.UserID, &ms.LastRead, &ms.LastDelivered); err != nil {
				statusRows.Close()
				http.Error(w, "Ошибка чтения статуса: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if ms.UserID == userID {
				myLastRead = ms.LastRead
			}
			c.MemberStatus = append(c.MemberStatus, ms)
		}
		statusRows.Close()

		// Unread = messages newer than my read pointer, excluding my own + system.
		if err := database.DB.QueryRow(`
			SELECT COUNT(*) FROM messages
			WHERE chat_id = $1 AND id > $2 AND sender_id <> $3 AND sender_id <> 0 AND deleted_at IS NULL`,
			c.ID, myLastRead, userID,
		).Scan(&c.UnreadCount); err != nil {
			http.Error(w, "Ошибка подсчёта непрочитанных: "+err.Error(), http.StatusInternalServerError)
			return
		}

		if c.MemberStatus == nil {
			c.MemberStatus = []models.MemberStatus{}
		}

		chats = append(chats, c)
	}

	if chats == nil {
		chats = []models.ChatDetail{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(chats)
}

func CreateChatHandler(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title     string `json:"title"`
		Type      string `json:"type"`
		MemberIDs []int  `json:"member_ids"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Некорректный формат JSON", http.StatusBadRequest)
		return
	}

	if input.Type != "direct" && input.Type != "group" {
		http.Error(w, "Поле type должно быть 'direct' или 'group'", http.StatusBadRequest)
		return
	}

	if len(input.MemberIDs) == 0 {
		http.Error(w, "Необходимо указать хотя бы одного участника в member_ids", http.StatusBadRequest)
		return
	}

	for _, uid := range input.MemberIDs {
		if uid == 0 {
			http.Error(w, "Нельзя создать чат с системным пользователем", http.StatusBadRequest)
			return
		}
	}

	var chatID int
	err := database.DB.QueryRow(
		`INSERT INTO chats (title, type) VALUES ($1, $2) RETURNING id`,
		input.Title, input.Type,
	).Scan(&chatID)
	if err != nil {
		http.Error(w, "Ошибка создания чата: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for _, uid := range input.MemberIDs {
		if _, err := database.DB.Exec(
			`INSERT INTO chat_members (chat_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			chatID, uid,
		); err != nil {
			http.Error(w, "Ошибка добавления участника: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Insert a system (service) message so the chat has an activity timestamp
	// from creation. Sent from the reserved system user (id 0), which is never
	// a chat member. Non-fatal: the chat already exists if this fails.
	creatorID, _ := r.Context().Value(middleware.UserIDKey).(int)
	var creatorName string
	if err := database.DB.QueryRow(
		`SELECT COALESCE(NULLIF(display_name, ''), username) FROM users WHERE id = $1`,
		creatorID,
	).Scan(&creatorName); err != nil {
		creatorName = "Someone"
	}

	var sysText string
	if input.Type == "group" {
		sysText = fmt.Sprintf("%s created the group", creatorName)
	} else {
		sysText = "Chat started"
	}
	if _, err := database.DB.Exec(
		`INSERT INTO messages (sender_id, chat_id, text) VALUES (0, $1, $2)`,
		chatID, sysText,
	); err != nil {
		log.Printf("Не удалось создать системное сообщение для чата %d: %v", chatID, err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]int{"chat_id": chatID})
}
