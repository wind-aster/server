package models

import "time"

type User struct {
	ID          int       `json:"id"`
	Email       string    `json:"email"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Password    string    `json:"password,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
