package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
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
	if result.Reply != "" {
		resp["reply"] = result.Reply
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
	p := getConfigPath()
	result := map[string]any{"path": p}

	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			result["status"] = "not_found"
			result["message"] = fmt.Sprintf("文件不存在: %s", p)
		} else if os.IsPermission(err) {
			result["status"] = "no_permission"
			result["message"] = fmt.Sprintf("无权限访问: %s", p)
		} else {
			result["status"] = "error"
			result["message"] = err.Error()
		}
	} else {
		if info.IsDir() {
			result["status"] = "error"
			result["message"] = fmt.Sprintf("路径是目录而非文件: %s", p)
		} else {
			f, errW := os.OpenFile(p, os.O_WRONLY, 0)
			if errW != nil {
				if os.IsPermission(errW) {
					result["status"] = "read_only"
					result["message"] = fmt.Sprintf("文件只读，无写入权限: %s", p)
				} else {
					result["status"] = "ok"
				}
			} else {
				f.Close()
				result["status"] = "ok"
			}
		}
	}

	jsonResp(w, 200, result)
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

	info, err := os.Stat(p)
	status := "ok"
	message := ""
	if err != nil {
		if os.IsNotExist(err) {
			status = "not_found"
			message = fmt.Sprintf("文件不存在: %s", p)
		} else if os.IsPermission(err) {
			status = "no_permission"
			message = fmt.Sprintf("无权限访问: %s", p)
			jsonErr(w, 403, message)
			return
		}
	} else if info.IsDir() {
		jsonErr(w, 400, fmt.Sprintf("路径是目录而非文件: %s", p))
		return
	} else {
		f, errW := os.OpenFile(p, os.O_WRONLY, 0)
		if errW != nil && os.IsPermission(errW) {
			status = "read_only"
			message = fmt.Sprintf("文件只读，无写入权限: %s", p)
		} else if errW == nil {
			f.Close()
		}
	}

	setConfigPath(p)
	appDB.Exec(
		"INSERT INTO settings (key, value) VALUES ('config_path', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		p,
	)
	resp := map[string]any{"ok": true, "path": p, "status": status}
	if message != "" {
		resp["message"] = message
	}
	jsonResp(w, 200, resp)
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

// handleGetReloadConfig 读取 openclaw.json 中的 gateway.reload 配置
func handleGetReloadConfig(w http.ResponseWriter, r *http.Request) {
	config, err := readConfigFile()
	if err != nil {
		jsonResp(w, 200, map[string]any{"mode": "hybrid", "debounceMs": 300})
		return
	}

	mode := "hybrid"
	debounceMs := 300

	if gw, ok := config["gateway"].(map[string]any); ok {
		if rl, ok := gw["reload"].(map[string]any); ok {
			if m, ok := rl["mode"].(string); ok {
				mode = m
			}
			if d, ok := rl["debounceMs"].(float64); ok {
				debounceMs = int(d)
			}
		}
	}

	jsonResp(w, 200, map[string]any{"mode": mode, "debounceMs": debounceMs})
}

// handleSetReloadConfig 将 gateway.reload 配置写入 openclaw.json
func handleSetReloadConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode       string `json:"mode"`
		DebounceMs *int   `json:"debounceMs"`
	}
	parseBody(r, &body)

	mode := strings.TrimSpace(body.Mode)
	validModes := map[string]bool{"hybrid": true, "hot": true, "restart": true, "off": true}
	if !validModes[mode] {
		jsonErr(w, 400, "无效的 reload 模式，可选: hybrid, hot, restart, off")
		return
	}

	debounceMs := 300
	if body.DebounceMs != nil {
		debounceMs = *body.DebounceMs
		if debounceMs < 0 {
			debounceMs = 0
		}
	}

	p := getConfigPath()
	if _, err := os.Stat(p); os.IsNotExist(err) {
		jsonErr(w, 404, fmt.Sprintf("配置文件不存在: %s", p))
		return
	}

	config, err := readConfigFile()
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}

	gw := ensureMap(config, "gateway")
	rl := ensureMap(gw, "reload")
	rl["mode"] = mode
	rl["debounceMs"] = debounceMs

	if _, err := writeConfigFile(config); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}

	jsonResp(w, 200, map[string]any{"ok": true, "mode": mode, "debounceMs": debounceMs})
}

func parseConfigApplyParams(r *http.Request) (configApplyParams, error) {
	var body configApplyParams
	parseBody(r, &body)

	body.Primary = strings.TrimSpace(body.Primary)
	if body.Primary == "" {
		return body, fmt.Errorf("必须选择主要模型 (primary)")
	}
	if body.Fallbacks == nil {
		body.Fallbacks = []string{}
	}
	if body.Reload != nil {
		validModes := map[string]bool{"hybrid": true, "hot": true, "restart": true, "off": true}
		if !validModes[body.Reload.Mode] {
			body.Reload.Mode = "hybrid"
		}
		if body.Reload.DebounceMs < 0 {
			body.Reload.DebounceMs = 0
		}
	}
	return body, nil
}

func handlePreviewConfig(w http.ResponseWriter, r *http.Request) {
	params, err := parseConfigApplyParams(r)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}

	currentJSON, newJSON, err := previewConfigChanges(params)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}

	jsonResp(w, 200, map[string]any{
		"ok":       true,
		"current":  currentJSON,
		"proposed": newJSON,
		"filename": filepath.Base(getConfigPath()),
	})
}

func handleApplyConfig(w http.ResponseWriter, r *http.Request) {
	params, err := parseConfigApplyParams(r)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}

	result, err := applyConfigToFile(params)
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	result["ok"] = true
	jsonResp(w, 200, result)
}

// ---------------------------------------------------------------------------
// Gateway 状态管理
// ---------------------------------------------------------------------------

func handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	running, pid := checkGatewayRunning()
	result := map[string]any{"running": running}
	if pid > 0 {
		result["pid"] = pid
	}
	jsonResp(w, 200, result)
}

func handleGatewayRestart(w http.ResponseWriter, r *http.Request) {
	running, pid := checkGatewayRunning()
	if running && pid > 0 {
		proc, err := os.FindProcess(pid)
		if err == nil {
			proc.Signal(os.Interrupt)
			time.Sleep(2 * time.Second)
			stillRunning, _ := checkGatewayRunning()
			if stillRunning {
				proc.Kill()
				time.Sleep(500 * time.Millisecond)
			}
		}
	}

	cmd := findGatewayCommand()
	if cmd == "" {
		jsonErr(w, 500, "未找到 OpenClaw Gateway 可执行文件，无法重启。请确认 Gateway 已安装并在 PATH 中。")
		return
	}

	execCmd := exec.Command(cmd)
	execCmd.Dir = filepath.Dir(getConfigPath())
	execCmd.Stdout = nil
	execCmd.Stderr = nil
	if err := execCmd.Start(); err != nil {
		jsonErr(w, 500, fmt.Sprintf("启动 Gateway 失败: %v", err))
		return
	}

	time.Sleep(1 * time.Second)
	nowRunning, newPid := checkGatewayRunning()
	jsonResp(w, 200, map[string]any{"ok": true, "running": nowRunning, "pid": newPid})
}

func checkGatewayRunning() (bool, int) {
	myPid := os.Getpid()
	out, err := exec.Command("pgrep", "-f", "openclaw").Output()
	if err != nil || len(out) == 0 {
		return false, 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid == myPid {
			continue
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		cmd := strings.ToLower(string(cmdline))
		if strings.Contains(cmd, "openclawswitch") {
			continue
		}
		return true, pid
	}
	return false, 0
}

func findGatewayCommand() string {
	names := []string{"openclaw", "openclaw-gateway"}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
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
