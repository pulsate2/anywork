package aiprofile

import (
	"encoding/json"
	"errors"
	"net/http"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error, code int) {
	http.Error(w, err.Error(), code)
}

// statusFor 依据错误类型映射 HTTP 状态。invalidErr 是入参问题(名字空、配置
// 格式不对),得让前端看到原始文案,所以一并映射成 400。
func statusFor(err error) int {
	var inv invalidErr
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrExists):
		return http.StatusConflict
	case errors.Is(err, ErrApp), errors.As(err, &inv):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}
