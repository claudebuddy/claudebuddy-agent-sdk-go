package agent

import "errors"

var (
	ErrInvalidOptions = errors.New("invalid agent options")
	ErrSessionBusy    = errors.New("session already has an active run")
	ErrAgentClosed    = errors.New("agent is closed")
)
