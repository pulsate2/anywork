package agent

import "encoding/json"

// codexTurnError 同时接受 v2 的 error 对象和旧协议的 error 字符串。
type codexTurnError struct {
	Message string
}

func (e *codexTurnError) UnmarshalJSON(raw []byte) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		return json.Unmarshal(raw, &e.Message)
	}
	var p struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	e.Message = p.Message
	return nil
}

type codexErrorNotification struct {
	Error        codexTurnError `json:"error"`
	Message      string         `json:"message"`
	WillRetry    *bool          `json:"willRetry"`
	OldWillRetry *bool          `json:"will_retry"`
	TurnID       string         `json:"turnId"`
	OldTurnID    string         `json:"turn_id"`
}

func (p codexErrorNotification) failure() codexTurnError {
	e := p.Error
	e.Message = firstNonEmpty(e.Message, p.Message)
	return e
}

func (p codexErrorNotification) retrying(defaultValue bool) bool {
	if p.WillRetry != nil {
		return *p.WillRetry
	}
	if p.OldWillRetry != nil {
		return *p.OldWillRetry
	}
	return defaultValue
}

// Native 重试只展示进度,保持 turnID/running,不能放行队列或打断计时。
func (d *codexDriver) handleCodexError(raw json.RawMessage, streamWarning bool) {
	var p codexErrorNotification
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	e := p.failure()
	if e.Message == "" {
		e.Message = "Codex 请求失败"
	}
	d.mu.Lock()
	id := firstNonEmpty(p.TurnID, p.OldTurnID)
	if id != "" && id == d.lastFinishedTurn {
		// 旧 task_complete 可能先于 v2 最终错误到达,保留迟到的错误但
		// 不能再发 idle 或改变当前回合状态。
		if !p.retrying(streamWarning) && !d.lastFinishedErrorReported {
			d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(e.Message, 4000)}})
			d.lastFinishedErrorReported = true
		}
		d.mu.Unlock()
		return
	}
	if id != "" && d.turnID != "" && id != d.turnID {
		d.mu.Unlock()
		return
	}
	d.lastTurnError = e
	if p.retrying(streamWarning) {
		d.emit(Event{Kind: KindSystemInfo, Payload: &SystemInfoPayload{Type: "api_error", Status: "retrying", Text: "Codex 正在自动重试", Error: truncate(e.Message, 4000)}})
		d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusRunning}})
	} else if !d.lastErrorReported {
		d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(e.Message, 4000)}})
		d.lastErrorReported = true
	}
	d.mu.Unlock()
}

// finishCodexTurn 仅处理真正的回合终态;Codex 内部重试不会走到这里。
// 新旧协议的重复终态只收尾一次,不再额外发消息重试。
func (d *codexDriver) finishCodexTurn(id, status string, e codexTurnError) {
	d.mu.Lock()
	failed := status == "failed" || status == "error"
	if id != "" && id == d.lastFinishedTurn {
		if failed && !d.lastFinishedErrorReported {
			if e.Message == "" {
				e.Message = "codex 回合失败"
			}
			d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(e.Message, 4000)}})
			d.lastFinishedErrorReported = true
		}
		d.mu.Unlock()
		return
	}
	if (id != "" && d.turnID != "" && id != d.turnID) ||
		(id == "" && d.turnID == "" && !d.turnActive) {
		d.mu.Unlock()
		return
	}
	if id == "" {
		id = d.turnID
	}
	if id != "" {
		d.lastFinishedTurn = id
	}
	d.turnID = ""
	d.turnActive = false
	if e.Message == "" {
		e.Message = d.lastTurnError.Message
	}
	if failed && e.Message == "" {
		e.Message = "codex 回合失败"
	}
	if failed && !d.lastErrorReported {
		d.emit(Event{Kind: KindError, Payload: &ErrorPayload{Message: truncate(e.Message, 4000)}})
	}
	d.lastFinishedErrorReported = d.lastErrorReported || failed
	d.lastTurnError = codexTurnError{}
	d.lastErrorReported = false
	d.turnEndLocked()
	d.emit(Event{Kind: KindStatus, Payload: &StatusPayload{State: StatusIdle}})
	d.mu.Unlock()
}
