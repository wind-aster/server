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
	ID           int       `json:"id"`
	Title        string    `json:"title"`
	Type         string    `json:"type"`
	Members      []User    `json:"members"`
	LastMessages []Message `json:"last_messages"`
}
