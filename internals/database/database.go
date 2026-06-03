package database

import (
	"database/sql"
	"log"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var DB *sql.DB

func InitDB(connStr string) {
	var err error

	DB, err = sql.Open("pgx", connStr)
	if err != nil {
		log.Fatalf("Не удалось настроить пул соединений: %v", err)
	}

	if err = DB.Ping(); err != nil {
		log.Fatalf("Контейнер с PostgreSQL недоступен: %v", err)
	}

	log.Println("Успешно подключились к PostgreSQL в Docker!")
	// createTables()
}

func createTables() {
	usersSchema := `
	CREATE TABLE IF NOT EXISTS users (
		id SERIAL PRIMARY KEY,
		email TEXT UNIQUE NOT NULL,
		username TEXT UNIQUE NOT NULL,
		display_name TEXT DEFAULT '',
		password TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`

	messagesSchema := `
	CREATE TABLE IF NOT EXISTS messages (
		id SERIAL PRIMARY KEY,
		sender_id INTEGER NOT NULL,
		receiver_id INTEGER NOT NULL,
		text TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(sender_id) REFERENCES users(id),
		FOREIGN KEY(receiver_id) REFERENCES users(id)
	);`

	if _, err := DB.Exec(usersSchema); err != nil {
		log.Fatalf("Ошибка создания таблицы users: %v", err)
	}
	if _, err := DB.Exec(messagesSchema); err != nil {
		log.Fatalf("Ошибка создания таблицы messages: %v", err)
	}

	log.Println("Таблицы базы данных успешно проверены/созданы.")
}
