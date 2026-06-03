package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/WindAster/server/internals/auth"
	"github.com/WindAster/server/internals/database"
	"golang.org/x/crypto/bcrypt"
)

func RegisterHandler(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email       string `json:"email"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Некорректный формат JSON", http.StatusBadRequest)
		return
	}

	if input.Email == "" || input.Username == "" || input.Password == "" {
		http.Error(w, "Все поля (email, username, password) обязательны", http.StatusBadRequest)
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Внутренняя ошибка при обработке пароля", http.StatusInternalServerError)
		return
	}

	query := `INSERT INTO users (email, username, display_name, password) VALUES ($1, $2, $3, $4)`

	_, err = database.DB.Exec(query, input.Email, input.Username, input.DisplayName, string(hashedPassword))
	if err != nil {
		http.Error(w, "Ошибка базы данных: "+err.Error(), http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	response := map[string]string{
		"status":  "success",
		"message": "Регистрация прошла успешно",
	}
	json.NewEncoder(w).Encode(response)
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email       string `json:"email"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Некорректный формат JSON", http.StatusBadRequest)
		return
	}

	if (input.Email == "" && input.Username == "") || input.Password == "" {
		http.Error(w, "Поля email/username и password обязательны", http.StatusBadRequest)
		return
	}

	var dbID int
	var dbUsername, dbEmail, dbHashedPassword string

	var query string
	var identifier string
	if input.Email != "" {
		query = `SELECT id, username, email, password FROM users WHERE email = $1`
		identifier = input.Email
	} else {
		query = `SELECT id, username, email, password FROM users WHERE username = $1`
		identifier = input.Username
	}

	err := database.DB.QueryRow(query, identifier).Scan(&dbID, &dbUsername, &dbEmail, &dbHashedPassword)
	if err != nil {
		http.Error(w, "Неверный email/username или пароль", http.StatusUnauthorized)
		return
	}

	err = bcrypt.CompareHashAndPassword([]byte(dbHashedPassword), []byte(input.Password))
	if err != nil {
		http.Error(w, "Неверный email/username или пароль", http.StatusUnauthorized)
		return
	}

	token, err := auth.GenerateToken(dbID)
	if err != nil {
		http.Error(w, "Ошибка генерации токена", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	response := map[string]interface{}{
		"status": "success",
		"token":  token,
		"user": map[string]interface{}{
			"id":       dbID,
			"username": dbUsername,
			"email":    dbEmail,
		},
	}
	json.NewEncoder(w).Encode(response)
}
