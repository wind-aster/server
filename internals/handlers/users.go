package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/models"
)

func GetUsersHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := database.DB.Query(`SELECT id, username, display_name, email FROM users WHERE id <> 0 ORDER BY username ASC`)
	if err != nil {
		http.Error(w, "Ошибка получения пользователей: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email); err != nil {
			http.Error(w, "Ошибка чтения данных пользователя: "+err.Error(), http.StatusInternalServerError)
			return
		}
		users = append(users, u)
	}

	if users == nil {
		users = []models.User{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}
