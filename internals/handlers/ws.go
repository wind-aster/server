package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/WindAster/server/internals/auth"
	"github.com/WindAster/server/internals/database"
	"github.com/gorilla/websocket"
)

// Настройка апгрейдера (CheckOrigin true нужен для тестов, чтобы не ругался CORS)
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Хранилище активных сокетов: мапим UserID -> Соединение
var (
	clients      = make(map[int]*websocket.Conn)
	clientsMutex sync.RWMutex
)

// WsHandler обрабатывает постоянное подключение пользователя
func WsHandler(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	userID, err := auth.ValidateToken(tokenStr)
	if err != nil || userID == 0 {
		http.Error(w, "Недействительный или отсутствующий токен", http.StatusUnauthorized)
		return
	}

	// Апгрейдим HTTP до WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Ошибка апгрейда соединения для юзера %d: %v", userID, err)
		return
	}

	// Регистрируем клиента в нашей карте онлайна
	clientsMutex.Lock()
	clients[userID] = conn
	clientsMutex.Unlock()
	log.Printf("Пользователь %d подключился по WebSocket", userID)

	// Гарантируем очистку при отключении
	defer func() {
		clientsMutex.Lock()
		delete(clients, userID)
		clientsMutex.Unlock()
		conn.Close()
		log.Printf("Пользователь %d отключился", userID)
	}()

	// Цикл бесконечного чтения сообщений от этого клиента
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Соединение с юзером %d разорвано: %v", userID, err)
			break
		}

		// Парсим входящий JSON
		var incoming struct {
			ChatID        int    `json:"chat_id"`
			Text          string `json:"text"`
			AttachmentIDs []int  `json:"attachment_ids"`
			ReplyToID     *int   `json:"reply_to_id"`
		}

		if err := json.Unmarshal(payload, &incoming); err != nil {
			log.Printf("Не удалось распарсить JSON от юзера %d: %v", userID, err)
			continue
		}

		// A message must carry text or at least one attachment.
		if incoming.Text == "" && len(incoming.AttachmentIDs) == 0 {
			continue
		}

		// Persist + broadcast (the sender is a chat member, so the fanout below
		// delivers the message back to them as well).
		if _, err := createMessage(userID, incoming.ChatID, incoming.Text, incoming.AttachmentIDs, incoming.ReplyToID); err != nil {
			log.Printf("Ошибка сохранения WS-сообщения в базу: %v", err)
			continue
		}
	}
}

// broadcastToChat sends payload to every currently-online member of chatID.
func broadcastToChat(chatID int, payload []byte) {
	rows, err := database.DB.Query(`SELECT user_id FROM chat_members WHERE chat_id = $1`, chatID)
	if err != nil {
		log.Printf("broadcast: не удалось получить участников чата %d: %v", chatID, err)
		return
	}
	var memberIDs []int
	for rows.Next() {
		var mID int
		if err := rows.Scan(&mID); err == nil {
			memberIDs = append(memberIDs, mID)
		}
	}
	rows.Close()

	clientsMutex.RLock()
	defer clientsMutex.RUnlock()
	for _, mID := range memberIDs {
		if conn, online := clients[mID]; online {
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				log.Printf("broadcast: ошибка отправки пользователю %d: %v", mID, err)
			}
		}
	}
}

// broadcastEvent marshals a typed envelope {"type": eventType, ...fields} and
// fans it out to a chat. This is the single shape all realtime events use.
func broadcastEvent(chatID int, eventType string, fields map[string]interface{}) {
	env := map[string]interface{}{"type": eventType}
	for k, v := range fields {
		env[k] = v
	}
	payload, err := json.Marshal(env)
	if err != nil {
		log.Printf("broadcast: не удалось сериализовать событие %q: %v", eventType, err)
		return
	}
	broadcastToChat(chatID, payload)
}
