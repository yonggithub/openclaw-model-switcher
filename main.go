package main

import (
	_ "embed"
	"log"
	"net/http"
)

//go:embed templates/index.html
var indexHTML []byte

func main() {
	initDB()
	defer appDB.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", serveIndex)

	mux.HandleFunc("GET /api/providers", handleListProviders)
	mux.HandleFunc("POST /api/providers", handleCreateProvider)
	mux.HandleFunc("PUT /api/providers/{id}", handleUpdateProvider)
	mux.HandleFunc("DELETE /api/providers/{id}", handleDeleteProvider)
	mux.HandleFunc("POST /api/providers/{id}/fetch", handleFetchProviderModels)

	mux.HandleFunc("GET /api/models", handleListModels)
	mux.HandleFunc("POST /api/providers/{id}/models", handleAddModel)
	mux.HandleFunc("DELETE /api/models/{id}", handleDeleteModel)
	mux.HandleFunc("PUT /api/models/{id}/toggle", handleToggleModel)
	mux.HandleFunc("POST /api/models/batch-select", handleBatchSelectModels)
	mux.HandleFunc("POST /api/models/test", handleTestModel)
	mux.HandleFunc("POST /api/models/batch-test", handleBatchTestModels)

	mux.HandleFunc("GET /api/agents", handleListAgents)
	mux.HandleFunc("PUT /api/agents/{id}/model", handleUpdateAgentModel)

	mux.HandleFunc("GET /api/config/path", handleGetConfigPath)
	mux.HandleFunc("POST /api/config/path", handleSetConfigPath)
	mux.HandleFunc("GET /api/config", handleGetConfig)
	mux.HandleFunc("POST /api/config/preview", handlePreviewConfig)
	mux.HandleFunc("POST /api/config/apply", handleApplyConfig)
	mux.HandleFunc("GET /api/config/reload", handleGetReloadConfig)
	mux.HandleFunc("POST /api/config/reload", handleSetReloadConfig)

	mux.HandleFunc("GET /api/gateway/status", handleGatewayStatus)
	mux.HandleFunc("POST /api/gateway/restart", handleGatewayRestart)

	addr := ":8356"
	log.Printf("OpenClawSwitch 启动: http://0.0.0.0%s\n", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
