package aiprofile

import (
	"encoding/json"
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

// statusFor 依据错误类型映射 HTTP 状态。
func statusFor(err error) int {
	if err == ErrNotFound {
		return http.StatusNotFound
	}
	if err == ErrExists {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}
