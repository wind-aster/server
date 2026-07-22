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

// wsClient wraps a single socket. gorilla/websocket forbids concurrent writers,
// so every write goes through writeMu.
type wsClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func (c *wsClient) write(payload []byte) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		log.Printf("ws: ошибка записи: %v", err)
	}
}

// clients maps a userID to the set of their live connections (one per tab).
var (
	clients      = make(map[int]map[*wsClient]bool)
	clientsMutex sync.RWMutex
)

// addClient registers a connection, reporting whether it's the user's first
// (i.e. they just came online).
func addClient(userID int, c *wsClient) bool {
	clientsMutex.Lock()
	defer clientsMutex.Unlock()
	set := clients[userID]
	if set == nil {
		set = make(map[*wsClient]bool)
		clients[userID] = set
	}
	first := len(set) == 0
	set[c] = true
	return first
}

// removeClient unregisters a connection, reporting whether it was the user's
// last (i.e. they just went offline).
func removeClient(userID int, c *wsClient) bool {
	clientsMutex.Lock()
	defer clientsMutex.Unlock()
	set := clients[userID]
	if set == nil {
		return false
	}
	delete(set, c)
	if len(set) == 0 {
		delete(clients, userID)
		return true
	}
	return false
}

func isOnline(userID int) bool {
	clientsMutex.RLock()
	defer clientsMutex.RUnlock()
	return len(clients[userID]) > 0
}

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

	client := &wsClient{conn: conn}
	if addClient(userID, client) {
		// First live connection — the user just came online.
		announcePresence(userID, true)
	}
	log.Printf("Пользователь %d подключился по WebSocket", userID)

	// Now that this user is online, any messages sent while they were offline
	// count as delivered — advance their delivered pointer across all chats.
	markDeliveredAllChats(userID)

	// Гарантируем очистку при отключении
	defer func() {
		if removeClient(userID, client) {
			// Last connection closed — the user went offline.
			touchLastSeen(userID)
			announcePresence(userID, false)
		}
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
			Type          string `json:"type"`
			ChatID        int    `json:"chat_id"`
			Text          string `json:"text"`
			AttachmentIDs []int  `json:"attachment_ids"`
			ReplyToID     *int   `json:"reply_to_id"`
			Typing        bool   `json:"typing"`
		}

		if err := json.Unmarshal(payload, &incoming); err != nil {
			log.Printf("Не удалось распарсить JSON от юзера %d: %v", userID, err)
			continue
		}

		// Ephemeral typing signal — fan out to the chat, don't persist.
		if incoming.Type == "typing" {
			broadcastEvent(incoming.ChatID, "typing", map[string]interface{}{
				"chat_id": incoming.ChatID,
				"user_id": userID,
				"typing":  incoming.Typing,
			})
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

// broadcastToChat sends payload to every live connection of every chat member.
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

	broadcastToUsers(memberIDs, payload)
}

// broadcastToUsers sends payload to every live connection of the given users.
// Targets are collected under the lock, then written without holding it (so a
// slow socket can't block the map).
func broadcastToUsers(userIDs []int, payload []byte) {
	clientsMutex.RLock()
	var targets []*wsClient
	for _, uid := range userIDs {
		for c := range clients[uid] {
			targets = append(targets, c)
		}
	}
	clientsMutex.RUnlock()

	for _, c := range targets {
		c.write(payload)
	}
}

// markDeliveredToOnline marks every currently-online chat member (except the
// sender) as delivered up to msgID, so a just-sent message reflects delivery to
// recipients who have a live socket.
func markDeliveredToOnline(chatID, msgID, senderID int) {
	rows, err := database.DB.Query(`SELECT user_id FROM chat_members WHERE chat_id = $1`, chatID)
	if err != nil {
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

	for _, mID := range memberIDs {
		if mID == senderID {
			continue
		}
		if isOnline(mID) {
			markDelivered(chatID, mID, msgID)
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
