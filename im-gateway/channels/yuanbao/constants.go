package yuanbao

// Constants for the Tencent Yuanbao channel (ports constants.py).

import "time"

const (
	DefaultAPIDomain = "https://bot.yuanbao.tencent.com"
	DefaultWSURL     = "wss://bot-wss.yuanbao.tencent.com/wss/connection"
	SignTokenPath    = "/api/v5/robotLogic/sign-token"

	ModuleConnAccess = "conn_access"
	ModuleBiz        = "yuanbao_openclaw_proxy"

	CmdAuthBind             = "auth-bind"
	CmdPing                 = "ping"
	CmdKickout              = "kickout"
	CmdUpdateMeta           = "update-meta"
	CmdInboundMessage       = "inbound_message"
	CmdSendC2CMessage       = "send_c2c_message"
	CmdSendGroupMessage     = "send_group_message"
	CmdSendPrivateHeartbeat = "send_private_heartbeat"
	CmdSendGroupHeartbeat   = "send_group_heartbeat"

	CallbackC2CSendMsg   = "C2C.CallbackAfterSendMsg"
	CallbackGroupSendMsg = "Group.CallbackAfterSendMsg"

	MsgTypeText  = "TIMTextElem"
	MsgTypeImage = "TIMImageElem"
	MsgTypeFile  = "TIMFileElem"
	MsgTypeSound = "TIMSoundElem"
	MsgTypeVideo = "TIMVideoFileElem"

	CmdTypeRequest  = 0
	CmdTypeResponse = 1
	CmdTypePush     = 2
	CmdTypePushAck  = 3

	RetSuccess     = 0
	RetAlreadyAuth = 41101
)

// TokenExpiredCodes are the auth-bind failure codes that indicate the token
// must be refreshed before the next attempt.
var TokenExpiredCodes = map[int]bool{41103: true, 41104: true, 41108: true}

const (
	HeartbeatRunning = 1
	HeartbeatFinish  = 2
	// HermesInstanceID is the default instance id (str()'d into configs).
	HermesInstanceID = 17

	wtVarint = 0
	wtLen    = 2
)

// MIMEToImageFormat maps a MIME type onto the Yuanbao image format code.
var MIMEToImageFormat = map[string]int{
	"image/jpeg": 1,
	"image/jpg":  1,
	"image/gif":  2,
	"image/png":  3,
	"image/bmp":  4,
}

// cst is the UTC+8 zone used for the sign-token timestamp (Python _CST).
var cst = time.FixedZone("CST", 8*3600)

const (
	tokenRefreshMarginSeconds = 300.0
	inboundDedupeTTLSeconds   = 300.0

	uploadInfoPath       = "/api/resource/genUploadInfo"
	resourceDownloadPath = "/api/resource/v1/download"
	maxMediaSizeBytes    = 50 * 1024 * 1024
)
