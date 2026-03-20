package main

import (
	"database/sql"
	"log"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	appDB         *sql.DB
	configPathMu  sync.RWMutex
	configPathVal string
)

const defaultConfigPath = "openclaw.json"

func getConfigPath() string {
	configPathMu.RLock()
	defer configPathMu.RUnlock()
	return configPathVal
}

func setConfigPath(p string) {
	configPathMu.Lock()
	defer configPathMu.Unlock()
	configPathVal = p
}

func initDB() {
	var err error
	appDB, err = sql.Open("sqlite", "openclawswitch.db")
	if err != nil {
		log.Fatalf("无法打开数据库: %v", err)
	}

	appDB.Exec("PRAGMA journal_mode=WAL")
	appDB.Exec("PRAGMA foreign_keys=ON")

	_, err = appDB.Exec(`
		CREATE TABLE IF NOT EXISTS providers (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT    NOT NULL UNIQUE,
			base_url   TEXT    NOT NULL,
			api_key    TEXT    DEFAULT '',
			api_type   TEXT    DEFAULT 'openai-completions',
			created_at TEXT    DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS models (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_id INTEGER NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
			model_id    TEXT    NOT NULL,
			owned_by    TEXT    DEFAULT '',
			selected    INTEGER DEFAULT 0,
			created_at  TEXT    DEFAULT (datetime('now')),
			UNIQUE(provider_id, model_id)
		);
		CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`)
	if err != nil {
		log.Fatalf("初始化数据库表失败: %v", err)
	}

	var path string
	err = appDB.QueryRow("SELECT value FROM settings WHERE key = 'config_path'").Scan(&path)
	if err == nil {
		setConfigPath(path)
	} else {
		setConfigPath(defaultConfigPath)
		appDB.Exec("INSERT INTO settings (key, value) VALUES ('config_path', ?)", defaultConfigPath)
	}
}
