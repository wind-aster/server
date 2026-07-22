package handlers

import (
	"encoding/json"
	"log"
	"time"

	"github.com/WindAster/server/internals/database"
)

// sharedChatUserIDs returns the distinct users who share at least one chat with
// the given user (the audience for their presence changes).
func sharedChatUserIDs(userID int) []int {
	rows, err := database.DB.Query(
		`SELECT DISTINCT cm2.user_id
		 FROM chat_members cm1
		 JOIN chat_members cm2 ON cm1.chat_id = cm2.chat_id
		 WHERE cm1.user_id = $1 AND cm2.user_id <> $1`,
		userID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// touchLastSeen records the user's last-seen timestamp (called when they go
// fully offline).
func touchLastSeen(userID int) {
	if _, err := database.DB.Exec(
		`UPDATE users SET last_seen_at = now() WHERE id = $1`, userID,
	); err != nil {
		log.Printf("presence: не удалось обновить last_seen для %d: %v", userID, err)
	}
}

// announcePresence notifies a user's chat partners that they came online or went
// offline. Going offline carries the fresh last_seen timestamp.
func announcePresence(userID int, online bool) {
	env := map[string]interface{}{
		"type":    "presence",
		"user_id": userID,
		"online":  online,
	}
	if !online {
		env["last_seen"] = time.Now()
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return
	}
	broadcastToUsers(sharedChatUserIDs(userID), payload)
}
