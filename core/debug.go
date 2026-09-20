package core

import "sync/atomic"

// verboseHTTP 控制是否把 HTTP 请求/响应原文与消息内容输出到日志。
//
// 默认关闭，且建议在生产环境保持关闭。
//
// 这些内容在 AI 场景下天然携带业务数据，与普通 API 的"参数"性质不同：
//   - 请求体通常直接嵌入业务文档（提示词全文、base64 图片）
//   - 响应体是模型的解析结果
//   - 请求头带有 Authorization 凭证
//
// 默认落盘会造成业务数据泄露与凭据泄露，因此这里选择"默认不输出"，
// 由使用方在排查问题时显式开启。
//
// 即使开启，Authorization 等凭证头也会被脱敏（见 http.go 的 dumpRequest）。
var verboseHTTP atomic.Bool

// SetVerboseHTTP 开启或关闭 HTTP 调试输出。
func SetVerboseHTTP(v bool) { verboseHTTP.Store(v) }

// VerboseHTTP 报告 HTTP 调试输出是否开启。
func VerboseHTTP() bool { return verboseHTTP.Load() }
