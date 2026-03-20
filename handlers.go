package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// JSON 响应辅助
// ---------------------------------------------------------------------------

func jsonResp(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	jsonResp(w, status, map[string]string{"error": msg})
}

func parseBody(r *http.Request, v any) {
	if r.Body == nil {
		return
	}
	defer r.Body.Close()
	json.NewDecoder(r.Body).Decode(v)
}

// ---------------------------------------------------------------------------
// 页面
// ---------------------------------------------------------------------------

func serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// ---------------------------------------------------------------------------
// 服务商 CRUD
// ---------------------------------------------------------------------------

func handleListProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := appDB.Query(`
		SELECT p.id, p.name, p.base_url, p.api_key, p.api_type, p.created_at,
		       COUNT(m.id) AS model_count,
		       COALESCE(SUM(CASE WHEN m.selected = 1 THEN 1 ELSE 0 END), 0) AS selected_count
		FROM providers p
		LEFT JOIN models m ON m.provider_id = p.id
		GROUP BY p.id
		ORDER BY p.created_at
	`)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var id, modelCount, selectedCount int
		var name, baseURL, apiKey, apiType, createdAt string
		if err := rows.Scan(&id, &name, &baseURL, &apiKey, &apiType, &createdAt, &modelCount, &selectedCount); err != nil {
			continue
		}
		result = append(result, map[string]any{
			"id": id, "name": name, "base_url": baseURL, "api_key": apiKey,
			"api_type": apiType, "created_at": createdAt,
			"model_count": modelCount, "selected_count": selectedCount,
		})
	}
	jsonResp(w, 200, result)
}

func handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		APIType string `json:"api_type"`
	}
	parseBody(r, &body)

	name := strings.TrimSpace(body.Name)
	baseURL := strings.TrimSpace(body.BaseURL)
	apiKey := strings.TrimSpace(body.APIKey)
	apiType := strings.TrimSpace(body.APIType)
	if apiType == "" {
		apiType = "openai-completions"
	}

	if name == "" || baseURL == "" {
		jsonErr(w, 400, "服务商名称和 Base URL 为必填项")
		return
	}

	_, err := appDB.Exec(
		"INSERT INTO providers (name, base_url, api_key, api_type) VALUES (?, ?, ?, ?)",
		name, baseURL, apiKey, apiType,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			jsonErr(w, 409, fmt.Sprintf("服务商 '%s' 已存在", name))
		} else {
			jsonErr(w, 500, err.Error())
		}
		return
	}
	jsonResp(w, 201, map[string]any{"ok": true})
}

func handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonErr(w, 400, "无效的服务商 ID")
		return
	}

	var exists int
	if err := appDB.QueryRow("SELECT COUNT(1) FROM providers WHERE id = ?", pid).Scan(&exists); err != nil || exists == 0 {
		jsonErr(w, 404, "服务商不存在")
		return
	}

	var body map[string]any
	parseBody(r, &body)

	var fields []string
	var values []any
	for _, col := range []string{"name", "base_url", "api_key", "api_type"} {
		if v, ok := body[col]; ok {
			fields = append(fields, col+" = ?")
			values = append(values, v)
		}
	}
	if len(fields) == 0 {
		jsonErr(w, 400, "没有需要更新的字段")
		return
	}

	values = append(values, pid)
	_, err = appDB.Exec(
		fmt.Sprintf("UPDATE providers SET %s WHERE id = ?", strings.Join(fields, ", ")),
		values...,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			jsonErr(w, 409, "服务商名称重复")
		} else {
			jsonErr(w, 500, err.Error())
		}
		return
	}
	jsonResp(w, 200, map[string]any{"ok": true})
}

func handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonErr(w, 400, "无效的服务商 ID")
		return
	}
	appDB.Exec("DELETE FROM providers WHERE id = ?", pid)
	jsonResp(w, 200, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 从服务商 API 拉取模型
// ---------------------------------------------------------------------------

func handleFetchProviderModels(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonErr(w, 400, "无效的服务商 ID")
		return
	}

	var baseURL, apiKey string
	err = appDB.QueryRow("SELECT base_url, api_key FROM providers WHERE id = ?", pid).Scan(&baseURL, &apiKey)
	if err != nil {
		jsonErr(w, 404, "服务商不存在")
		return
	}

	models, err := fetchModelsFromProvider(baseURL, apiKey)
	if err != nil {
		jsonErr(w, 502, err.Error())
		return
	}

	for _, m := range models {
		appDB.Exec(`
			INSERT INTO models (provider_id, model_id, owned_by) VALUES (?, ?, ?)
			ON CONFLICT(provider_id, model_id) DO UPDATE SET owned_by = excluded.owned_by`,
			pid, m.ModelID, m.OwnedBy,
		)
	}

	jsonResp(w, 200, map[string]any{"ok": true, "count": len(models)})
}

// ---------------------------------------------------------------------------
// 模型管理
// ---------------------------------------------------------------------------

func handleListModels(w http.ResponseWriter, r *http.Request) {
	rows, err := appDB.Query(`
		SELECT m.id, m.provider_id, m.model_id, m.owned_by, m.selected, m.created_at,
		       p.name AS provider_name
		FROM models m
		JOIN providers p ON m.provider_id = p.id
		ORDER BY p.name, m.model_id
	`)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	defer rows.Close()

	result := make([]map[string]any, 0)
	for rows.Next() {
		var id, providerID, selected int
		var modelID, ownedBy, createdAt, providerName string
		if err := rows.Scan(&id, &providerID, &modelID, &ownedBy, &selected, &createdAt, &providerName); err != nil {
			continue
		}
		result = append(result, map[string]any{
			"id": id, "provider_id": providerID, "model_id": modelID,
			"owned_by": ownedBy, "selected": selected, "created_at": createdAt,
			"provider_name": providerName,
		})
	}
	jsonResp(w, 200, result)
}

func handleToggleModel(w http.ResponseWriter, r *http.Request) {
	mid, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		jsonErr(w, 400, "无效的模型 ID")
		return
	}

	appDB.Exec("UPDATE models SET selected = 1 - selected WHERE id = ?", mid)

	var selected int
	appDB.QueryRow("SELECT selected FROM models WHERE id = ?", mid).Scan(&selected)
	jsonResp(w, 200, map[string]any{"ok": true, "selected": selected != 0})
}

func handleBatchSelectModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs      []int `json:"ids"`
		Selected bool  `json:"selected"`
	}
	body.Selected = true
	parseBody(r, &body)

	if len(body.IDs) == 0 {
		jsonErr(w, 400, "缺少模型 ID 列表")
		return
	}

	sel := 0
	if body.Selected {
		sel = 1
	}

	placeholders := make([]string, len(body.IDs))
	args := []any{sel}
	for i, id := range body.IDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	appDB.Exec(
		fmt.Sprintf("UPDATE models SET selected = ? WHERE id IN (%s)", strings.Join(placeholders, ",")),
		args...,
	)
	jsonResp(w, 200, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// 模型测试
// ---------------------------------------------------------------------------

func handleTestModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ModelKey string `json:"model_key"`
	}
	parseBody(r, &body)

	modelKey := strings.TrimSpace(body.ModelKey)
	if modelKey == "" || !strings.Contains(modelKey, "/") {
		jsonErr(w, 400, "无效的模型标识")
		return
	}

	parts := strings.SplitN(modelKey, "/", 2)
	providerName, modelID := parts[0], parts[1]

	var baseURL, apiKey string
	err := appDB.QueryRow("SELECT base_url, api_key FROM providers WHERE name = ?", providerName).Scan(&baseURL, &apiKey)
	if err != nil {
		jsonResp(w, 200, map[string]any{"ok": false, "error": fmt.Sprintf("服务商 '%s' 不存在", providerName), "latency_ms": 0})
		return
	}

	result := testSingleModel(providerName, baseURL, apiKey, modelID)

	resp := map[string]any{
		"ok":         result.OK,
		"latency_ms": result.LatencyMs,
	}
	if result.OK {
		resp["status_code"] = 200
	} else {
		resp["error"] = result.Error
	}
	jsonResp(w, 200, resp)
}

func handleBatchTestModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ModelKeys []string `json:"model_keys"`
	}
	parseBody(r, &body)

	if len(body.ModelKeys) == 0 {
		jsonErr(w, 400, "model_keys 不能为空")
		return
	}

	type provInfo struct {
		Name    string
		BaseURL string
		APIKey  string
	}
	cache := map[string]*provInfo{}

	type task struct {
		prov    *provInfo
		modelID string
	}
	var tasks []task

	for _, key := range body.ModelKeys {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		pname, mid := parts[0], parts[1]
		if _, ok := cache[pname]; !ok {
			var baseURL, apiKey string
			err := appDB.QueryRow("SELECT base_url, api_key FROM providers WHERE name = ?", pname).Scan(&baseURL, &apiKey)
			if err != nil {
				cache[pname] = nil
				continue
			}
			cache[pname] = &provInfo{Name: pname, BaseURL: baseURL, APIKey: apiKey}
		}
		if p := cache[pname]; p != nil {
			tasks = append(tasks, task{p, mid})
		}
	}

	maxWorkers := min(8, max(1, len(tasks)))
	results := make([]testResult, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxWorkers)

	for i, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, tk task) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = testSingleModel(tk.prov.Name, tk.prov.BaseURL, tk.prov.APIKey, tk.modelID)
		}(i, t)
	}
	wg.Wait()

	okCount := 0
	for _, r := range results {
		if r.OK {
			okCount++
		}
	}
	jsonResp(w, 200, map[string]any{"results": results, "total": len(results), "ok_count": okCount})
}

// ---------------------------------------------------------------------------
// Agent 管理
// ---------------------------------------------------------------------------

func handleListAgents(w http.ResponseWriter, r *http.Request) {
	config, err := readConfigFile()
	if err != nil {
		jsonResp(w, 200, []any{})
		return
	}

	agentsVal := getMapPath(config, "agents", "list")
	agentList, ok := agentsVal.([]any)
	if !ok {
		jsonResp(w, 200, []any{})
		return
	}

	primaryModel := ""
	if m := getMapPath(config, "agents", "defaults", "model", "primary"); m != nil {
		primaryModel, _ = m.(string)
	}

	result := make([]map[string]any, 0)
	for _, a := range agentList {
		ag, ok := a.(map[string]any)
		if !ok {
			continue
		}
		id := mapStr(ag, "id")
		name := mapStr(ag, "name")
		if name == "" {
			name = id
		}
		isMain := id == "main"
		model := ""
		if isMain {
			model = primaryModel
		} else {
			model = mapStr(ag, "model")
		}
		result = append(result, map[string]any{
			"id": id, "name": name, "model": model, "is_main": isMain,
		})
	}
	jsonResp(w, 200, result)
}

func handleUpdateAgentModel(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")

	var body struct {
		Model string `json:"model"`
	}
	parseBody(r, &body)
	newModel := strings.TrimSpace(body.Model)

	config, err := readConfigFile()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}

	agentsVal := getMapPath(config, "agents", "list")
	agentList, ok := agentsVal.([]any)
	if !ok {
		jsonErr(w, 404, fmt.Sprintf("Agent '%s' 不存在", agentID))
		return
	}

	found := false
	for _, a := range agentList {
		ag, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if mapStr(ag, "id") != agentID {
			continue
		}
		if agentID == "main" {
			ensureMap(ensureMap(ensureMap(config, "agents"), "defaults"), "model")["primary"] = newModel
		} else {
			if newModel != "" {
				ag["model"] = newModel
			} else {
				delete(ag, "model")
			}
		}
		found = true
		break
	}

	if !found {
		jsonErr(w, 404, fmt.Sprintf("Agent '%s' 不存在", agentID))
		return
	}

	if _, err := writeConfigFile(config); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonResp(w, 200, map[string]any{"ok": true, "agent_id": agentID, "model": newModel})
}

// ---------------------------------------------------------------------------
// 配置文件路径
// ---------------------------------------------------------------------------

func handleGetConfigPath(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, 200, map[string]any{"path": getConfigPath()})
}

func handleSetConfigPath(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	parseBody(r, &body)

	p := strings.TrimSpace(body.Path)
	if p == "" {
		jsonErr(w, 400, "路径不能为空")
		return
	}

	setConfigPath(p)
	appDB.Exec(
		"INSERT INTO settings (key, value) VALUES ('config_path', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		p,
	)
	jsonResp(w, 200, map[string]any{"ok": true, "path": p})
}

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	p := getConfigPath()
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			jsonErr(w, 404, fmt.Sprintf("文件不存在: %s", p))
		} else {
			jsonErr(w, 500, err.Error())
		}
		return
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonResp(w, 200, config)
}

func handleApplyConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Primary   string   `json:"primary"`
		Fallbacks []string `json:"fallbacks"`
	}
	parseBody(r, &body)

	primary := strings.TrimSpace(body.Primary)
	if primary == "" {
		jsonErr(w, 400, "必须选择主要模型 (primary)")
		return
	}

	fallbacks := body.Fallbacks
	if fallbacks == nil {
		fallbacks = []string{}
	}

	result, err := applyConfigToFile(primary, fallbacks)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	result["ok"] = true
	jsonResp(w, 200, result)
}

// ---------------------------------------------------------------------------
// 通用辅助
// ---------------------------------------------------------------------------

// getMapPath 沿着 key 链深入取值
func getMapPath(m map[string]any, keys ...string) any {
	var current any = m
	for _, key := range keys {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = cm[key]
	}
	return current
}
