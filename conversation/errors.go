package conversation

import "errors"

var (
	ErrNoChatToContinue    = errors.New("no interrupted chat to continue")
	ErrNoChatToRetry       = errors.New("no failed chat to retry")
	ErrMaxRoundsReached    = errors.New("maximum rounds reached")
	ErrParticipantNotFound = errors.New("participant not found")
	ErrChannelNotFound     = errors.New("channel not found")
	ErrEmptyGroup          = errors.New("channel has no members")
)
