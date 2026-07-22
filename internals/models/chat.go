package models

type Chat struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type ChatMember struct {
	ChatID int `json:"chat_id"`
	UserID int `json:"user_id"`
}

type ChatDetail struct {
	ID           int            `json:"id"`
	Title        string         `json:"title"`
	Type         string         `json:"type"`
	Members      []User         `json:"members"`
	LastMessages []Message      `json:"last_messages"`
	UnreadCount  int            `json:"unread_count"`
	MemberStatus []MemberStatus `json:"member_status"`
}

// MemberStatus reports how far a chat member has read/received, powering unread
// badges and delivery/read receipts.
type MemberStatus struct {
	UserID        int `json:"user_id"`
	LastRead      int `json:"last_read"`
	LastDelivered int `json:"last_delivered"`
}
