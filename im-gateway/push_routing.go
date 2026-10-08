package imgateway

// Shared helpers for proactive (bot-initiated) push routing.

// EphemeralPushMeta lists the fields that tie a send to a single inbound
// turn — they must not survive cron/webhook push.
var EphemeralPushMeta = []string{
	"msg_id",
	"message_id",
	"_frame",
	"_ws_client",
	"response_url",
	"context_token",
	"webhook_url",
}

// AliasSubjectFields backfills routing metadata keys from subjectID when
// absent.
func AliasSubjectFields(meta map[string]any, subjectID string, fieldNames ...string) map[string]any {
	if subjectID == "" {
		return meta
	}
	for _, name := range fieldNames {
		if _, ok := meta[name]; !ok {
			meta[name] = subjectID
		}
	}
	return meta
}

// StripEphemeralPushMeta removes passive-reply-only fields so proactive sends
// use the right path.
func StripEphemeralPushMeta(meta map[string]any) map[string]any {
	for _, key := range EphemeralPushMeta {
		delete(meta, key)
	}
	return meta
}
