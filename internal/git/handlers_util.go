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
	// 428:唯一一个"补齐前置条件后原样重试"的错误,前端靠这个码认出身份弹框,
	// 别和 400(输入不对,重试也没用)混在一起。
	case errors.Is(err, ErrNoIdentity):
		code = http.StatusPreconditionRequired
	case errors.Is(err, errEmptyMessage), errors.Is(err, errBadIdentity),
		errors.Is(err, errInitNotDir), errors.Is(err, errBranchExists):
		code = http.StatusBadRequest
	// 409:目录已经是仓库(或在某个仓库里),init 这个动作本身没有意义了。
	// 不归 400 是想让前端能分辨"你不必再点了,刷新就能看到仓库"这一种。
	case errors.Is(err, errAlreadyRepo):
		code = http.StatusConflict
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
