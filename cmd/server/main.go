package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/WindAster/server/internals/database"
	"github.com/WindAster/server/internals/handlers"
	"github.com/WindAster/server/internals/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/joho/godotenv"
)

func main() {
	fmt.Println("Запуск сервера WindAster...")

	godotenv.Load()

	connStr := os.Getenv("DB_DSN")
	if connStr == "" {
		fmt.Println("Ошибка: DB_DSN не задан")
		os.Exit(1)
	}

	database.InitDB(connStr)

	fmt.Println("Бэкенд успешно запущен и ожидает логики!")

	r := chi.NewRouter()

	r.Use(cors.Handler(cors.Options{
   		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Content-Type", "Authorization"},
	}))

	r.Post("/api/register", handlers.RegisterHandler)
	r.Post("/api/login", handlers.LoginHandler)

	r.Group(func(r chi.Router) {
		r.Use(middleware.AuthMiddleware)

		r.Get("/api/users", handlers.GetUsersHandler)

		r.Get("/api/chats", handlers.GetChatsHandler)
		r.Post("/api/chats", handlers.CreateChatHandler)

		r.Post("/api/messages", handlers.SendMessageHandler)
		r.Get("/api/messages", handlers.GetMessagesHandler)
	})

	r.Get("/api/ws", handlers.WsHandler)

	fmt.Println("HTTP-сервер запущен на http://localhost:8080")
	if err := http.ListenAndServe(":8080", r); err != nil {
		fmt.Printf("Ошибка запуска сервера: %v\n", err)
	}
}
