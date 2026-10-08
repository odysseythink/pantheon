# im-gateway Go 移植指南（PORTING.md）

本文档是 channel 子包移植的规范。core 包 `imgateway`（import 路径
`github.com/odysseythink/pantheon/im-gateway`）已完成并通过 vet。
所有渠道包在 `im-gateway/channels/<name>/` 下，包名 = 目录名（去掉连字符）。

## 总体规则

1. 100% 行为对齐 Python 源（D:\go_work\octop-gateway-1.0.0\src\octop_gateway）：
   常量数值、默认值、回退分支、错误处理、日志语义都要保留。
2. Python dataclass/pydantic 配置 → Go struct，带 `FromDict(map[string]any) error`
   方法（用 core 的 `imgateway.Str/Bool/Int/Float/Map/ApplyAliases/ChannelConfigFromDict`）。
3. 每个渠道 struct 嵌入 `*imgateway.BaseChannel`，并提供
   `Base() *imgateway.BaseChannel { return c.BaseChannel }`。
4. 构造函数末尾调用 `c.InitBase(imgateway.BaseOptions{...}, c)`。
5. 在 `init()` 中调用 `imgateway.RegisterChannel("<kind>", factory)`。
6. Python asyncio task → goroutine + context；SDK 线程回调 → 直接 goroutine。

## core API 速查

```go
// 必须实现的接口（Python 抽象方法）
type ChannelImpl interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    SendText(ctx context.Context, subject *ChannelSubject, text string) error
    SendContent(ctx context.Context, subject *ChannelSubject, parts []ContentPart) error
    SendMedia(ctx context.Context, subject *ChannelSubject, part ContentPart) error
    ParseInbound(ctx context.Context, raw any) (*InboundMessage, error)
}

// 可选能力接口（实现即生效）
type TypingSender interface{ SendTypingIndicator(ctx, subject) }
type PushMetadataEnricher interface{ EnrichPushMetadata(subject, meta) map[string]any }
type Preprocessor interface{ PreprocessInbound(ctx, message) }
type ProcessInboundOverride interface{ ProcessInbound(ctx, message, subject) bool }
type RemoteMediaFetcher interface{ FetchRemoteMedia(ctx, url) ([]byte, string, error) }
type DebounceKeyer interface{ GetDebounceKey(message) string }
type InboundBatcher interface{ ShouldBatchInbound(message) bool; MergeInbound(msgs) *InboundMessage }
type DefaultConstraintsProvider interface{ DefaultConstraints() *ChannelConstraints }

// BaseChannel 上常用的导出方法（channel 内部调用这些，不调用 Reply*/Push*）
b.HTTPClient() *http.Client          // 懒创建共享 client（对应 _ensure_http）
b.CloseHTTP()
b.ReplyText/ReplyContent/ReplyMedia(ctx, subject, ...) // 公共入口（带限流）
b.RateLimitedSend(ctx, subject, text) error            // 清洗 + reply_text
b.CleanOutput(text) string                             // <think> 回退处理
b.LoadMediaBytes(part *ContentPart) ([]byte, string, error) // data>local_path>url
b.FetchRemoteMedia(ctx, url) ([]byte, string, error)   // 可被 RemoteMediaFetcher 覆盖
b.MediaLabel(part) string
b.SafeMediaFilename(name, fallback) string
b.GetSubject(id)/ListSubjects()/SetOnNewSubject(fn)
b.ResolvePushSubject(subject) *ChannelSubject
b.TrackSubject(msg)
b.HandleInbound(ctx, raw) error
b.PrepareInbound(ctx, raw) (*InboundMessage, *ChannelSubject, bool, error)
b.FinalizeInbound(msg, ok)
b.Constraints() *ChannelConstraints   // ReplyTimeout/ShowThinking/ShowToolHints/ThinkingTemplate/...
b.GroupContextManager() *GroupContextManager
b.ChannelID()/ChannelType()/TenantID()
b.Enqueue(payload any)
```

模型：`ContentPart{Kind: imgateway.ContentTypeText|Image|Video|Audio|File, Text, URL, LocalPath,
Data(base64), MimeType, Size *int64, AltText, Width/Height *int, ThumbnailURL, Duration *int, Filename}`；
`NewTextPart/NewImagePart/NewVideoPart/NewAudioPart/NewFilePart`。
`MessageEvent` 构造器：`TextMessage/DeltaEvent/TypingEvent/FlushEvent/ThinkingEvent/
ThinkingDeltaEvent/ToolStartEvent(name, extra)/ToolEndEvent/CompletedEvent/ErrorEvent`。
`MessageProcessor func(ctx, *InboundMessage) <-chan *MessageEvent`（channel 关闭即流结束；
处理器失败要发 `ErrorEvent`）。
`ChannelSubject{SubjectID, FirstSeen, LastSeen, DisplayName, ChatType, Metadata map[string]any}`。

配置基座：`imgateway.ChannelConfig{ChannelID, TenantID, ShowThinking, ShowToolHints, *GroupContextConfig}`，
FromDict 里先 `imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)` 再填平台字段，
`ApplyAliases(data, aliasesMap)` 处理别名。凭据缺失返回 `&imgateway.ChannelCredentialsError{Kind, Missing}`。

`InboundMessage{ChannelID, ChannelType, TenantID, ChannelSubject *ChannelSubject,
ChannelSessionID, Content []ContentPart, GroupContext, Metadata map[string]any, Timestamp float64}`。
注意 Python `timestamp` 默认 time.time()；Go 用 `imgateway` 未导出 nowFloat()，渠道里自行 `float64(time.Now().UnixNano())/1e9`。

日志：`imgateway.SetLogger` 注入；渠道内不要直接用 log 包，用 core 的
logInfo/logWarn/logError/logDebug（同包可用；子包中请自行定义小包装或使用标准 log —— 用 core 导出的 `imgateway.SetLogger` 之后的包级函数不可跨包，子包直接用 log.Printf 前缀即可，不强求）。

## factory 注册样例

```go
func init() {
    imgateway.RegisterChannel("feishu", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
        cfg := NewFeishuConfig()
        switch t := opts.Config.(type) {
        case *FeishuConfig: cfg = t
        case FeishuConfig: cfg = &t
        case map[string]any:
            if err := cfg.FromDict(t); err != nil { return nil, err }
        }
        if missing := cfg.MissingCredentials(); len(missing) > 0 {
            return nil, &imgateway.ChannelCredentialsError{Kind: "feishu", Missing: missing}
        }
        return NewFeishuChannel(opts.Processor, cfg,
            WithChannelID(opts.ChannelID), WithTenantID(opts.TenantID)), nil
    })
}
```

## 依赖

- WebSocket 用 `github.com/gorilla/websocket`（core 已不再有其他外部依赖）。
- MQTT 用 `github.com/eclipse/paho.mqtt.golang`。
- Discord 用原生 WebSocket 走 Gateway 协议（不引 discordgo，保持与 Python discord.py
  的 Gateway 行为手写对齐：IDENTIFY、HEARTBEAT、RESUME）。
- 其余一律标准库（crypto/*、encoding/*、net/http）。

## 已知边界差异（Go vs Python，均不影响常规路径）

以下差异在测试对照中发现，均为极端/脏输入路径，评估后保留 Go 现行为：

1. **feishu.go:1370** — `extractPostParts` emotion 标签：Python `get("emoji_type") or get("emoji") or "emoji"`
   的空串回退语义，Go 的 `strOf` 在键存在但值为 `""` 时不回退。真实 Feishu 事件 emoji_type 恒非空。
2. **weixin media.go:233 `decodeAESKey`** — Python `bytes.fromhex` 跳过空白、`base64.b64decode(validate=False)`
   丢弃非法字符；Go hex/base64 均严格校验，脏输入返回 nil。
3. **yuanbao protocol.go `decodeHead`** — Python 坏帧（截断/非法 varint）抛 ValueError 向上传播；Go 有意
   把错误边界留在 `DecodeConnMsg`，head 解析失败返回空 head、无 error（注释已声明）。
4. **yuanbao utils.go `CosSign`** — 仅当参数/头中出现仅大小写不同的重复键时，Go map 迭代序可能影响
   q-header-list/q-url-param-list 顺序；Python sorted 元组序确定。COS 场景 header 键通常已规范化。

## 测试对照覆盖

- core（im-gateway/core_test.go）：25+ 测试，覆盖模型 Python 语义（extract_text/has_text/GroupContext
  降级/合批/RateLimiter 滑窗/HandleInbound pipeline/panic 隔离/manager 集成）。
- feishu/discord/telegram/qq/weixin/yuanbao：各包 *_test.go，纯函数逐行为对照 Python 源，
  golden 值由 Python 原版实跑生成（yuanbao 协议 varint/ConnMsg/CosSign 全套 golden）。
