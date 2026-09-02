package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/WindAster/server/internals/auth"
	"github.com/WindAster/server/internals/config"
	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/handlers"
	"github.com/WindAster/server/internals/middleware"
	"github.com/WindAster/server/internals/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load()

	cfg := config.Load()

	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := database.MigrateUp(cfg.DBDSN); err != nil {
			log.Fatalf("migrate: %v", err)
		}
		log.Println("migrations applied")
		return
	}

	fmt.Println("Запуск сервера WindAster...")

	if cfg.DBDSN == "" {
		fmt.Println("Ошибка: DB_DSN не задан")
		os.Exit(1)
	}
	if cfg.JWTSecret == "" {
		log.Fatal("Ошибка: JWT_SECRET не задан")
	}

	auth.Configure(cfg.JWTSecret, cfg.AccessTokenTTL)
	handlers.Cfg = cfg

	database.InitDB(cfg.DBDSN)

	// Object storage (MinIO). Fatal if unreachable — uploads depend on it.
	store, err := storage.NewMinio(cfg)
	if err != nil {
		log.Fatalf("Не удалось инициализировать хранилище: %v", err)
	}
	handlers.Store = store
	handlers.MaxUploadSize = cfg.MaxUploadSize
	handlers.MaxVideoSize = cfg.MaxVideoSize
	handlers.MediaURLExpiry = cfg.MediaURLExpiry

	// Periodically clean up abandoned (never-sent) uploads from DB + storage.
	handlers.StartAttachmentCleanup(time.Hour, cfg.PendingAttachmentTTL)

	fmt.Println("Бэкенд успешно запущен и ожидает логики!")

	r := chi.NewRouter()

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
	}))

	r.Post("/api/register", handlers.RegisterHandler)
	r.Post("/api/login", handlers.LoginHandler)
	r.Post("/api/refresh", handlers.RefreshHandler)
	r.Post("/api/logout", handlers.LogoutHandler)

	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware)

		r.Get("/api/users", handlers.GetUsersHandler)

		r.Get("/api/chats", handlers.GetChatsHandler)
		r.Post("/api/chats", handlers.CreateChatHandler)
		r.Post("/api/chats/{id}/read", handlers.ReadChatHandler)

		r.Post("/api/messages", handlers.SendMessageHandler)
		r.Get("/api/messages", handlers.GetMessagesHandler)
		r.Patch("/api/messages/{id}", handlers.EditMessageHandler)
		r.Delete("/api/messages/{id}", handlers.DeleteMessageHandler)
		r.Post("/api/messages/{id}/reactions", handlers.ToggleReactionHandler)

		r.Post("/api/uploads", handlers.CreateUploadHandler)
		r.Get("/api/files/{id}", handlers.GetFileHandler)
	})

	r.Get("/api/ws", handlers.WsHandler)

	fmt.Println("HTTP-сервер запущен на http://localhost:8080")
	if err := http.ListenAndServe(":8080", r); err != nil {
		fmt.Printf("Ошибка запуска сервера: %v\n", err)
	}
}
