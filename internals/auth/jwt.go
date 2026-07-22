package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Configured at startup via Configure(). No insecure compile-time default.
var (
	jwtSecret []byte
	accessTTL time.Duration
)

// Configure sets the signing secret and access-token lifetime. Called once
// from main after config is loaded.
func Configure(secret string, ttl time.Duration) {
	jwtSecret = []byte(secret)
	accessTTL = ttl
}

type claims struct {
	UserID int `json:"user_id"`
	jwt.RegisteredClaims
}

// GenerateAccessToken mints a short-lived signed JWT for the given user.
func GenerateAccessToken(userID int) (string, error) {
	c := claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(accessTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return token.SignedString(jwtSecret)
}

func ValidateToken(tokenStr string) (int, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil {
		return 0, err
	}

	c, ok := token.Claims.(*claims)
	if !ok || !token.Valid {
		return 0, errors.New("invalid token")
	}
	return c.UserID, nil
}

// GenerateRefreshToken returns a fresh opaque refresh token (32 random bytes,
// base64url-encoded). The raw value is returned to the client once; only its
// hash is persisted.
func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the hex-encoded SHA-256 of a raw token, used for storage
// and lookup so raw refresh tokens never touch the database.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
