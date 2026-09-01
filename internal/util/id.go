// Package util 提供跨模块的小工具。
package util

import (
	"crypto/rand"
	"encoding/hex"
)

// ID 生成 16 字节随机 hex 标识,用作各表主键。
func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // 熵源故障,进程无法安全运行
	}
	return hex.EncodeToString(b)
}
