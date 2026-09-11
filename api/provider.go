package api

import "context"

type MessageProvider interface {
	CreateMessage(context.Context, MessagesRequest) (*StreamMessage, error)
	CreateMessageStream(context.Context, MessagesRequest) (<-chan StreamEvent, <-chan error)
}
