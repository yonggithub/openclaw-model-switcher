// CLI 工具：从远程 API 拉取模型列表，按 provider (owned_by) 更新 openclaw.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	cliDefaultConfigPath = "/root/.openclaw/openclaw.json"
	modelsURL            = "https://cliproxy.tgoo.top:8088/v1/models"
	apiKey               = "pccw-sk-2wpT5ZHtNAVoiXR54"
)

type cliModelEntry struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Reasoning     bool         `json:"reasoning"`
	Input         []string     `json:"input"`
	Cost          cliModelCost `json:"cost"`
	ContextWindow int          `json:"contextWindow"`
	MaxTokens     int          `json:"maxTokens"`
}

type cliModelCost struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

func newCliModelEntry(id string) cliModelEntry {
	return cliModelEntry{
		ID: id, Name: id,
		Reasoning: false, Input: []string{"text"},
		Cost: cliModelCost{}, ContextWindow: 200000, MaxTokens: 8192,
	}
}

func strVal(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func fetchModels() ([]any, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	auths := []struct {
		name    string
		headers map[string]string
	}{
		{"Authorization: Bearer", map[string]string{"Authorization": "Bearer " + apiKey}},
		{"x-api-key", map[string]string{"x-api-key": apiKey}},
	}

	var lastStatus int
	for _, auth := range auths {
		req, _ := http.NewRequest("GET", modelsURL, nil)
		for k, v := range auth.headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("  请求失败 (%s): %v\n", auth.name, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastStatus = resp.StatusCode

		if resp.StatusCode == 200 {
			var data any
			json.Unmarshal(body, &data)
			switch v := data.(type) {
			case []any:
				return v, nil
			case map[string]any:
				if d, ok := v["data"].([]any); ok {
					return d, nil
				}
				return nil, fmt.Errorf("无法解析响应结构，期望 list 或 {data: list}，得到: %T", data)
			default:
				return nil, fmt.Errorf("无法解析响应结构，期望 list 或 {data: list}，得到: %T", data)
			}
		}
		fmt.Printf("  %s 返回 %d，尝试下一种认证…\n", auth.name, resp.StatusCode)
	}
	return nil, fmt.Errorf("无法获取模型列表：两种认证均未返回 200，最后状态码: %d", lastStatus)
}

type orderedKey struct {
	Provider string
	ID       string
}

func buildPerProviderModels(rawList []any) (map[string][]cliModelEntry, []orderedKey) {
	providerModels := map[string][]cliModelEntry{}
	var orderedKeys []orderedKey

	for _, item := range rawList {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mid := strVal(m, "id")
		if mid == "" {
			mid = strVal(m, "model")
		}
		if mid == "" {
			mid = strVal(m, "name")
		}
		if mid == "" {
			continue
		}
		provider := strings.TrimSpace(strVal(m, "owned_by"))
		if provider == "" {
			provider = "openai"
		}
		providerModels[provider] = append(providerModels[provider], newCliModelEntry(mid))
		orderedKeys = append(orderedKeys, orderedKey{provider, mid})
	}
	return providerModels, orderedKeys
}

func updateConfig(config map[string]any, providerModels map[string][]cliModelEntry, orderedKeys []orderedKey) {
	if config["models"] == nil {
		config["models"] = map[string]any{}
	}
	modelsSection := config["models"].(map[string]any)
	if modelsSection["providers"] == nil {
		modelsSection["providers"] = map[string]any{}
	}
	providers := modelsSection["providers"].(map[string]any)

	openaiRef, _ := providers["openai"].(map[string]any)
	copyKeys := []string{"baseUrl", "apiKey", "api", "mode"}

	for provider, modelsList := range providerModels {
		if providers[provider] == nil {
			providers[provider] = map[string]any{}
		}
		dest := providers[provider].(map[string]any)
		if openaiRef != nil {
			for _, k := range copyKeys {
				if _, exists := dest[k]; !exists {
					if v, ok := openaiRef[k]; ok {
						dest[k] = v
					}
				}
			}
		}
		dest["models"] = modelsList
	}

	newModelsMap := map[string]any{}
	for _, ok := range orderedKeys {
		newModelsMap[ok.Provider+"/"+ok.ID] = map[string]any{}
	}
	if len(newModelsMap) == 0 {
		return
	}

	if config["agents"] == nil {
		config["agents"] = map[string]any{}
	}
	agents := config["agents"].(map[string]any)
	if agents["defaults"] == nil {
		agents["defaults"] = map[string]any{}
	}
	defaults := agents["defaults"].(map[string]any)
	defaults["models"] = newModelsMap

	if defaults["model"] == nil {
		defaults["model"] = map[string]any{}
	}
	modelCfg := defaults["model"].(map[string]any)
	currentPrimary, _ := modelCfg["primary"].(string)
	if currentPrimary == "" || newModelsMap[currentPrimary] == nil {
		modelCfg["primary"] = orderedKeys[0].Provider + "/" + orderedKeys[0].ID
	}
}

func main() {
	dryRun := flag.Bool("dry-run", false, "仅打印摘要，不写文件")
	flag.Parse()

	configPath := cliDefaultConfigPath
	if flag.NArg() > 0 {
		configPath = flag.Arg(0)
	}

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		fmt.Printf("错误：配置文件不存在: %s\n", configPath)
		os.Exit(1)
	}

	fmt.Printf("读取配置: %s\n", configPath)
	data, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Printf("错误：无法读取文件: %v\n", err)
		os.Exit(1)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		fmt.Printf("错误：无法解析 JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("正在拉取模型列表…")
	rawList, err := fetchModels()
	if err != nil {
		fmt.Printf("错误：%v\n", err)
		os.Exit(1)
	}

	providerModels, orderedKeys := buildPerProviderModels(rawList)
	total := 0
	for _, v := range providerModels {
		total += len(v)
	}

	if total == 0 {
		fmt.Println("警告：未解析到任何模型，将不会更新 models 与 agents.defaults。")
		if *dryRun {
			fmt.Println("--dry-run：未写入文件。")
		}
		os.Exit(0)
	}

	limit := min(3, len(orderedKeys))
	first3IDs := make([]string, limit)
	first3Pairs := make([]string, limit)
	for i := 0; i < limit; i++ {
		first3IDs[i] = orderedKeys[i].ID
		first3Pairs[i] = orderedKeys[i].Provider + "/" + orderedKeys[i].ID
	}

	fmt.Printf("解析到 %d 个模型，来自 %d 个 provider。\n", total, len(providerModels))
	fmt.Printf("前 3 个 id: %v\n", first3IDs)
	fmt.Printf("前 3 个 provider/id: %v\n", first3Pairs)

	if *dryRun {
		fmt.Println("--dry-run：不写入文件。")
		return
	}

	updateConfig(config, providerModels, orderedKeys)

	backupName := fmt.Sprintf("openclaw.json.bak.%s", time.Now().Format("20060102150405"))
	backupPath := filepath.Join(filepath.Dir(configPath), backupName)
	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		fmt.Printf("错误：无法创建备份 %s: %v\n", backupPath, err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Printf("错误：JSON 编码失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(configPath, out, 0644); err != nil {
		fmt.Printf("错误：无法写入文件: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("已更新并保存: %s\n", configPath)
}
