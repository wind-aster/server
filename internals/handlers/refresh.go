package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/WindAster/server/internals/auth"
	"github.com/WindAster/server/internals/config"
	"github.com/WindAster/server/internals/database"
)

// Cfg holds the runtime config, wired from main() at startup (mirrors Store).
// Handlers read token TTLs from it.
var Cfg config.Config

// issueRefreshToken generates a fresh opaque refresh token, stores only its
// hash, and returns the raw value to hand to the client.
func issueRefreshToken(userID int, ttl time.Duration) (string, error) {
	raw, err := auth.GenerateRefreshToken()
	if err != nil {
		return "", err
	}
	_, err = database.DB.Exec(
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, auth.HashToken(raw), time.Now().Add(ttl),
	)
	if err != nil {
		return "", err
	}
	return raw, nil
}

// RefreshHandler rotates a refresh token: it validates the presented token,
// revokes it, issues a new refresh token + access token, and returns both.
// Presenting an already-spent (revoked/expired) token is treated as theft and
// revokes every session for that user.
func RefreshHandler(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.RefreshToken == "" {
		http.Error(w, "Отсутствует refresh-токен", http.StatusBadRequest)
		return
	}

	hash := auth.HashToken(input.RefreshToken)

	var (
		id        int
		userID    int
		expiresAt time.Time
		revokedAt sql.NullTime
	)
	err := database.DB.QueryRow(
		`SELECT id, user_id, expires_at, revoked_at FROM refresh_tokens WHERE token_hash = $1`,
		hash,
	).Scan(&id, &userID, &expiresAt, &revokedAt)
	if err != nil {
		// Unknown token — nothing to revoke, just reject.
		http.Error(w, "Недействительный refresh-токен", http.StatusUnauthorized)
		return
	}

	// Reuse detection: a spent token being replayed means it may be stolen.
	if revokedAt.Valid || time.Now().After(expiresAt) {
		database.DB.Exec(
			`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`,
			userID,
		)
		http.Error(w, "Недействительный refresh-токен", http.StatusUnauthorized)
		return
	}

	// Rotate atomically: revoke the old token and mint a new one.
	tx, err := database.DB.Begin()
	if err != nil {
		http.Error(w, "Внутренняя ошибка", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1`, id); err != nil {
		http.Error(w, "Внутренняя ошибка", http.StatusInternalServerError)
		return
	}

	newRaw, err := auth.GenerateRefreshToken()
	if err != nil {
		http.Error(w, "Внутренняя ошибка", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, auth.HashToken(newRaw), time.Now().Add(Cfg.RefreshTokenTTL),
	); err != nil {
		http.Error(w, "Внутренняя ошибка", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "Внутренняя ошибка", http.StatusInternalServerError)
		return
	}

	access, err := auth.GenerateAccessToken(userID)
	if err != nil {
		http.Error(w, "Ошибка генерации токена", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"access_token":  access,
		"refresh_token": newRaw,
	})
}

// LogoutHandler revokes a refresh token. Idempotent — always 200, so the
// client can clear local state regardless.
func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err == nil && input.RefreshToken != "" {
		database.DB.Exec(
			`UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
			auth.HashToken(input.RefreshToken),
		)
	}
	w.WriteHeader(http.StatusOK)
}
