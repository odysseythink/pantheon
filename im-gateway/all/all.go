// Package all blank-imports every built-in channel package so the
// imgateway registry knows all ten channel kinds (mirrors the lazy
// BUILTIN_CHANNELS import of octop_gateway.channels.__init__).
//
// Usage:
//
//	import (
//	    imgateway "github.com/odysseythink/pantheon/im-gateway"
//	    _ "github.com/odysseythink/pantheon/im-gateway/all"
//	)
//
//	manager.AddChannel(ctx, "feishu", map[string]any{...}, imgateway.AddOptions{})
package all

import (
	_ "github.com/odysseythink/pantheon/im-gateway/channels/dingtalk"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/discord"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/feishu"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/mqtt"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/qq"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/telegram"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/wecom"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/weixin"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/xiaoyi"
	_ "github.com/odysseythink/pantheon/im-gateway/channels/yuanbao"
)
