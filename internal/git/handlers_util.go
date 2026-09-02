package git

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
)

// httpErr 把内部错误映射为 HTTP 状态。
func (h *Handlers) httpErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotRepo):
		code = http.StatusBadRequest
	case errors.Is(err, ErrReadOnly):
		code = http.StatusForbidden
	case errors.Is(err, os.ErrNotExist):
		code = http.StatusNotFound
	case errors.Is(err, errEmptyMessage):
		code = http.StatusBadRequest
	case errors.Is(err, errBadRefArg), errors.Is(err, errUnknownBranchOp),
		errors.Is(err, errUnknownStashOp), errors.Is(err, errUnknownWorktreeOp),
		errors.Is(err, errUnknownRestoreMode), errors.Is(err, errUnknownRevertOp),
		errors.Is(err, errNoPaths), errors.Is(err, errBadCommitArg),
		errors.Is(err, errBinaryFile):
		code = http.StatusBadRequest
	}
	http.Error(w, err.Error(), code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
