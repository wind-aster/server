package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/WindAster/server/internals/auth"
)

type ContextKey string

const UserIDKey ContextKey = "userID"

func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "Требуется авторизация", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(header, "Bearer ")
		userID, err := auth.ValidateToken(tokenStr)
		if err != nil {
			http.Error(w, "Недействительный токен", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), UserIDKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
