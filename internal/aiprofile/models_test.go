package aiprofile

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestModelEndpoints(t *testing.T) {
	for _, tc := range []struct {
		base string
		want []string
	}{
		{"https://example.com/", []string{"https://example.com/v1/models", "https://example.com/models"}},
		{"https://example.com/v1/", []string{"https://example.com/v1/models", "https://example.com/models"}},
		{"https://example.com/proxy/v1", []string{"https://example.com/proxy/v1/models", "https://example.com/proxy/models"}},
		{"https://example.com/proxy", []string{"https://example.com/proxy/v1/models", "https://example.com/proxy/models"}},
		{"https://example.com/models", []string{"https://example.com/models", "https://example.com/v1/models"}},
		{"https://example.com/v1/models?unused=1", []string{"https://example.com/v1/models", "https://example.com/models"}},
	} {
		t.Run(tc.base, func(t *testing.T) {
			got, err := modelEndpoints(tc.base)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	for _, base := range []string{"", "example.com", "file:///tmp/models", "https://key@example.com"} {
		if _, err := modelEndpoints(base); err == nil {
			t.Errorf("accepted invalid base URL %q", base)
		}
	}
}

func TestModelsFallbackAndAuthentication(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing model API authentication headers")
		}
		if r.URL.Path == "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"},{"id":"model-b"},{"id":""}]}`))
	}))
	defer upstream.Close()
	res := requestModels(t, upstream.URL, "test-key")
	if res.Code != http.StatusOK {
		t.Fatalf("status %d: %s", res.Code, res.Body.String())
	}
	var payload struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Models, []string{"model-b", "model-a"}) || !reflect.DeepEqual(paths, []string{"/v1/models", "/models"}) {
		t.Fatalf("unexpected models %v or paths %v", payload.Models, paths)
	}
}

func TestModelsFailuresDoNotExposeKey(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, "secret-test-key"},
		{"invalid-json", 200, "secret-test-key"},
		{"empty-list", 200, `{"data":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			res := requestModels(t, upstream.URL, "secret-test-key")
			if res.Code != http.StatusBadGateway || strings.Contains(res.Body.String(), "secret-test-key") {
				t.Fatalf("unexpected failure response %d: %s", res.Code, res.Body.String())
			}
		})
	}
}

func requestModels(t *testing.T, baseURL, key string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"baseUrl": baseURL, "apiKey": key, "app": "claude"})
	r := httptest.NewRequest(http.MethodPost, "/api/ai/models", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	NewHandlers(nil).Models(w, r)
	return w
}
