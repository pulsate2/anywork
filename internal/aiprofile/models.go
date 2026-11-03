package aiprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Models 使用表单中的接口和 Key 查询,无需先保存配置,也不依赖供应商的 CORS 设置。
func (h *Handlers) Models(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseURL string `json:"baseUrl"`
		APIKey  string `json:"apiKey"`
		App     string `json:"app"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "请求格式不正确", http.StatusBadRequest)
		return
	}
	endpoints, err := modelEndpoints(in.BaseURL)
	if err != nil {
		httpError(w, err, http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(in.APIKey)
	if key == "" || strings.ContainsAny(key, "\r\n") {
		http.Error(w, "请填写有效的 API Key", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	var failures []string
	for _, endpoint := range endpoints {
		models, err := fetchModels(ctx, client, endpoint, key, in.App)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"models": models})
			return
		}
		path, _ := url.Parse(endpoint)
		failures = append(failures, path.Path+": "+err.Error())
		if ctx.Err() != nil {
			break
		}
	}
	http.Error(w, "获取模型列表失败（"+strings.Join(failures, "；")+"）", http.StatusBadGateway)
}

func modelEndpoints(baseURL string) ([]string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return nil, fmt.Errorf("请填写有效的 HTTP 或 HTTPS 接口地址")
	}
	u.RawQuery, u.Fragment, u.RawPath = "", "", ""
	base := strings.TrimRight(u.Path, "/")
	base = strings.TrimSuffix(base, "/models")
	paths := []string{base + "/v1/models", base + "/models"}
	if strings.HasSuffix(base, "/v1") {
		paths = []string{base + "/models", strings.TrimSuffix(base, "/v1") + "/models"}
	} else if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/models") {
		paths[0], paths[1] = paths[1], paths[0]
	}
	endpoints := make([]string, 0, len(paths))
	for _, path := range paths {
		u.Path = path
		endpoints = append(endpoints, u.String())
	}
	return endpoints, nil
}

func fetchModels(ctx context.Context, client *http.Client, endpoint, key, app string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("接口地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if app == "claude" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	res, err := client.Do(req)
	if err != nil {
		// 不回传供应商正文或请求信息,避免把 Key 带进错误提示。
		return nil, fmt.Errorf("连接失败或超时")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("返回的模型列表格式不正确")
	}
	models := make([]string, 0, len(payload.Data))
	seen := make(map[string]bool)
	for _, model := range payload.Data {
		id := strings.TrimSpace(model.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("未返回可用模型")
	}
	return models, nil
}
