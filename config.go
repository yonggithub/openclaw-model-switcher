package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// modelEntry 对应 openclaw.json 中每个模型的配置结构
type modelEntry struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Reasoning     bool      `json:"reasoning"`
	Input         []string  `json:"input"`
	Cost          modelCost `json:"cost"`
	ContextWindow int       `json:"contextWindow"`
	MaxTokens     int       `json:"maxTokens"`
}

type modelCost struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

func newModelEntry(id string) modelEntry {
	return modelEntry{
		ID:            id,
		Name:          id,
		Reasoning:     false,
		Input:         []string{"text"},
		Cost:          modelCost{},
		ContextWindow: 200000,
		MaxTokens:     8192,
	}
}

func readConfigFile() (map[string]any, error) {
	p := getConfigPath()
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return config, nil
}

// writeConfigFile 备份当前配置文件后写入新内容，返回备份路径
func writeConfigFile(config map[string]any) (string, error) {
	p := getConfigPath()
	backupPath := ""

	existing, err := os.ReadFile(p)
	if err == nil {
		backupName := fmt.Sprintf("openclaw.json.bak.%s", time.Now().Format("20060102150405"))
		backupPath = filepath.Join(filepath.Dir(p), backupName)
		if backupPath == backupName {
			backupPath = backupName
		}
		os.WriteFile(backupPath, existing, 0644)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return backupPath, os.WriteFile(p, data, 0644)
}

type selectedModelRow struct {
	ModelID      string
	OwnedBy      string
	ProviderName string
	BaseURL      string
	APIKey       string
	APIType      string
}

func applyConfigToFile(primary string, fallbacks []string) (map[string]any, error) {
	p := getConfigPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return nil, fmt.Errorf("配置文件不存在: %s", p)
	}

	config, err := readConfigFile()
	if err != nil {
		return nil, err
	}

	rows, err := appDB.Query(`
		SELECT m.model_id, m.owned_by, p.name AS provider_name,
		       p.base_url, p.api_key, p.api_type
		FROM models m
		JOIN providers p ON m.provider_id = p.id
		WHERE m.selected = 1
		ORDER BY p.name, m.model_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var selectedRows []selectedModelRow
	for rows.Next() {
		var r selectedModelRow
		if err := rows.Scan(&r.ModelID, &r.OwnedBy, &r.ProviderName, &r.BaseURL, &r.APIKey, &r.APIType); err != nil {
			continue
		}
		selectedRows = append(selectedRows, r)
	}

	// 1) 重建 models.providers
	modelsSection := ensureMap(config, "models")
	modelsSection["mode"] = "replace"

	providersMap := map[string]any{}
	for _, row := range selectedRows {
		pname := row.ProviderName
		if _, exists := providersMap[pname]; !exists {
			providersMap[pname] = map[string]any{
				"baseUrl": row.BaseURL,
				"apiKey":  row.APIKey,
				"api":     row.APIType,
				"models":  []any{},
			}
		}
		prov := providersMap[pname].(map[string]any)
		models := prov["models"].([]any)
		prov["models"] = append(models, newModelEntry(row.ModelID))
	}
	modelsSection["providers"] = providersMap

	// 2) 重建 agents.defaults.models
	agentsSection := ensureMap(config, "agents")
	defaultsSection := ensureMap(agentsSection, "defaults")

	modelsMap := map[string]any{}
	for _, row := range selectedRows {
		key := row.ProviderName + "/" + row.ModelID
		modelsMap[key] = map[string]any{}
	}
	defaultsSection["models"] = modelsMap

	// 3) 设置 primary / fallbacks
	modelConfig := ensureMap(defaultsSection, "model")
	modelConfig["primary"] = primary
	modelConfig["fallbacks"] = fallbacks

	backupPath, err := writeConfigFile(config)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"providers_count": len(providersMap),
		"models_count":    len(selectedRows),
		"primary":         primary,
		"fallbacks":       fallbacks,
		"backup":          backupPath,
	}, nil
}

// ensureMap 确保 parent[key] 是一个 map[string]any，不存在则创建
func ensureMap(parent map[string]any, key string) map[string]any {
	v, ok := parent[key]
	if !ok || v == nil {
		m := map[string]any{}
		parent[key] = m
		return m
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	parent[key] = m
	return m
}
