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

type reloadSettings struct {
	Mode       string `json:"mode"`
	DebounceMs int    `json:"debounceMs"`
}

// configApplyParams 统一的配置应用参数
type configApplyParams struct {
	Primary     string            `json:"primary"`
	Fallbacks   []string          `json:"fallbacks"`
	Reload      *reloadSettings   `json:"reload"`
	AgentModels map[string]string `json:"agent_models"`
}

// buildNewConfig 根据参数构建新的配置对象（不写入文件）
func buildNewConfig(params configApplyParams) (map[string]any, int, int, error) {
	primary := params.Primary
	fallbacks := params.Fallbacks
	p := getConfigPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return nil, 0, 0, fmt.Errorf("配置文件不存在: %s", p)
	}

	config, err := readConfigFile()
	if err != nil {
		return nil, 0, 0, err
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
		return nil, 0, 0, err
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

	agentsSection := ensureMap(config, "agents")
	defaultsSection := ensureMap(agentsSection, "defaults")

	modelsMap := map[string]any{}
	for _, row := range selectedRows {
		key := row.ProviderName + "/" + row.ModelID
		modelsMap[key] = map[string]any{}
	}
	defaultsSection["models"] = modelsMap

	modelConfig := ensureMap(defaultsSection, "model")
	modelConfig["primary"] = primary
	modelConfig["fallbacks"] = fallbacks

	// 应用 reload 配置
	if params.Reload != nil {
		gw := ensureMap(config, "gateway")
		rl := ensureMap(gw, "reload")
		rl["mode"] = params.Reload.Mode
		rl["debounceMs"] = params.Reload.DebounceMs
	}

	// 应用非 main agent 的模型变更
	if len(params.AgentModels) > 0 {
		if agentsSec, ok := config["agents"].(map[string]any); ok {
			if listVal, ok := agentsSec["list"].([]any); ok {
				for _, item := range listVal {
					ag, ok := item.(map[string]any)
					if !ok {
						continue
					}
					id, _ := ag["id"].(string)
					if id == "" || id == "main" {
						continue
					}
					if model, exists := params.AgentModels[id]; exists {
						if model != "" {
							ag["model"] = model
						} else {
							delete(ag, "model")
						}
					}
				}
			}
		}
	}

	return config, len(providersMap), len(selectedRows), nil
}

func applyConfigToFile(params configApplyParams) (map[string]any, error) {
	config, provCount, modelCount, err := buildNewConfig(params)
	if err != nil {
		return nil, err
	}

	backupPath, err := writeConfigFile(config)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"providers_count": provCount,
		"models_count":    modelCount,
		"primary":         params.Primary,
		"fallbacks":       params.Fallbacks,
		"backup":          backupPath,
	}, nil
}

// previewConfigChanges 返回当前配置和预览配置的 JSON 字符串
func previewConfigChanges(params configApplyParams) (string, string, error) {
	p := getConfigPath()
	currentData, err := os.ReadFile(p)
	if err != nil {
		return "", "", err
	}

	var currentObj map[string]any
	json.Unmarshal(currentData, &currentObj)
	currentJSON, _ := json.MarshalIndent(currentObj, "", "  ")

	newConfig, _, _, err := buildNewConfig(params)
	if err != nil {
		return "", "", err
	}

	tmpJSON, _ := json.Marshal(newConfig)
	var normalizedNew map[string]any
	json.Unmarshal(tmpJSON, &normalizedNew)
	newJSON, _ := json.MarshalIndent(normalizedNew, "", "  ")

	return string(currentJSON), string(newJSON), nil
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
