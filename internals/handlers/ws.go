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
			ChatID int    `json:"chat_id"`
			Text   string `json:"text"`
		}

		if err := json.Unmarshal(payload, &incoming); err != nil {
			log.Printf("Не удалось распарсить JSON от юзера %d: %v", userID, err)
			continue
		}

		// 1. Сохраняем сообщение в базу данных, чтобы получить ID и дату создания
		query := `INSERT INTO messages (sender_id, chat_id, text) VALUES ($1, $2, $3) RETURNING id, created_at`
		var msgID int
		var createdAt string

		err = database.DB.QueryRow(query, userID, incoming.ChatID, incoming.Text).Scan(&msgID, &createdAt)
		if err != nil {
			log.Printf("Ошибка сохранения WS-сообщения в базу: %v", err)
			continue
		}

		// Формируем полноценный объект сообщения для отправки
		fullMessage := map[string]interface{}{
			"id":         msgID,
			"sender_id":  userID,
			"chat_id":    incoming.ChatID,
			"text":       incoming.Text,
			"created_at": createdAt,
		}

		finalJSON, _ := json.Marshal(fullMessage)

		// 2. Получаем из базы список ID всех участников этого чата
		memberQuery := `SELECT user_id FROM chat_members WHERE chat_id = $1`
		rows, err := database.DB.Query(memberQuery, incoming.ChatID)
		if err != nil {
			log.Printf("Ошибка получения участников чата %d: %v", incoming.ChatID, err)
			continue
		}

		var memberIDs []int
		for rows.Next() {
			var mID int
			if err := rows.Scan(&mID); err == nil {
				memberIDs = append(memberIDs, mID)
			}
		}
		rows.Close()

		// 3. Рассылаем сообщение всем участникам чата, кто сейчас в онлайне
		clientsMutex.RLock()
		for _, mID := range memberIDs {
			// Если мы хотим, чтобы отправитель тоже получил это сообщение в общем потоке,
			// оставляем его. Если нет — можно поставить условие: if mID == userID { continue }
			if rConn, online := clients[mID]; online {
				err = rConn.WriteMessage(websocket.TextMessage, finalJSON)
				if err != nil {
					log.Printf("Ошибка отправки сообщения пользователю %d: %v", mID, err)
				}
			}
		}
		clientsMutex.RUnlock()

		// Старый «Шаг 3» (отправку дубликата отправителю) мы просто удаляем,
		// так как цикл выше и так отправит ему сообщение, если он состоит в этом чате.
	}
}
