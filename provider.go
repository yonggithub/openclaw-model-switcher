package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func buildModelsURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/models"
	}
	return base + "/v1/models"
}

func buildChatURL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

type fetchedModel struct {
	ModelID string `json:"model_id"`
	OwnedBy string `json:"owned_by"`
}

func fetchModelsFromProvider(baseURL, apiKey string) ([]fetchedModel, error) {
	url := buildModelsURL(baseURL)
	client := &http.Client{Timeout: 30 * time.Second}

	type authMethod struct {
		name    string
		headers map[string]string
	}

	var auths []authMethod
	if apiKey != "" {
		auths = []authMethod{
			{"Bearer", map[string]string{"Authorization": "Bearer " + apiKey}},
			{"x-api-key", map[string]string{"x-api-key": apiKey}},
		}
	} else {
		auths = []authMethod{{"NoAuth", nil}}
	}

	var lastErr string
	for _, auth := range auths {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			lastErr = fmt.Sprintf("%s: %v", auth.name, err)
			continue
		}
		for k, v := range auth.headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Sprintf("%s: %v", auth.name, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			lastErr = fmt.Sprintf("%s 返回 %d", auth.name, resp.StatusCode)
			continue
		}

		var raw any
		if err := json.Unmarshal(body, &raw); err != nil {
			lastErr = fmt.Sprintf("JSON 解析失败: %v", err)
			continue
		}

		var list []any
		switch v := raw.(type) {
		case []any:
			list = v
		case map[string]any:
			if d, ok := v["data"].([]any); ok {
				list = d
			} else {
				return nil, fmt.Errorf("无法解析响应，期望 list 或 dict.data")
			}
		default:
			return nil, fmt.Errorf("无法解析响应，期望 list 或 dict.data，得到 %T", raw)
		}

		var result []fetchedModel
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			mid := mapStr(m, "id")
			if mid == "" {
				mid = mapStr(m, "model")
			}
			if mid == "" {
				mid = mapStr(m, "name")
			}
			if mid == "" {
				continue
			}
			result = append(result, fetchedModel{ModelID: mid, OwnedBy: mapStr(m, "owned_by")})
		}
		return result, nil
	}

	return nil, fmt.Errorf("无法获取模型列表: %s", lastErr)
}

// mapStr 从 map 安全取字符串值
func mapStr(m map[string]any, key string) string {
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

type testResult struct {
	ModelKey  string `json:"model_key"`
	OK        bool   `json:"ok"`
	LatencyMs int    `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

func testSingleModel(provName, baseURL, apiKey, modelID string) testResult {
	chatURL := buildChatURL(baseURL)
	modelKey := provName + "/" + modelID

	payload, _ := json.Marshal(map[string]any{
		"model":      modelID,
		"messages":   []map[string]string{{"role": "user", "content": "Return only OK."}},
		"max_tokens": 1,
		"stream":     false,
	})

	type authMethod struct {
		name    string
		headers map[string]string
	}

	var auths []authMethod
	if apiKey != "" {
		auths = []authMethod{
			{"Bearer", map[string]string{"Authorization": "Bearer " + apiKey, "Content-Type": "application/json"}},
			{"x-api-key", map[string]string{"x-api-key": apiKey, "Content-Type": "application/json"}},
		}
	} else {
		auths = []authMethod{{"NoAuth", map[string]string{"Content-Type": "application/json"}}}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	var lastErr string

	for _, auth := range auths {
		req, err := http.NewRequest("POST", chatURL, bytes.NewReader(payload))
		if err != nil {
			lastErr = err.Error()
			continue
		}
		for k, v := range auth.headers {
			req.Header.Set(k, v)
		}

		t0 := time.Now()
		resp, err := client.Do(req)
		latency := int(time.Since(t0).Milliseconds())

		if err != nil {
			if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "Timeout") {
				return testResult{ModelKey: modelKey, OK: false, Error: "请求超时 (30s)", LatencyMs: 30000}
			}
			lastErr = err.Error()
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			return testResult{ModelKey: modelKey, OK: true, LatencyMs: latency}
		}

		var respBody map[string]any
		json.Unmarshal(body, &respBody)

		errMsg := ""
		if errObj, ok := respBody["error"].(map[string]any); ok {
			if msg, ok := errObj["message"].(string); ok {
				errMsg = msg
			}
		} else if errStr, ok := respBody["error"].(string); ok {
			errMsg = errStr
		}
		if errMsg == "" {
			errMsg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		lastErr = errMsg
	}

	return testResult{ModelKey: modelKey, OK: false, Error: lastErr, LatencyMs: 0}
}
