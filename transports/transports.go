package transports

import (
	"context"
	"net/http"
)


type Transport interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
}

type AuthenticationError struct {}
func (e *AuthenticationError) Error() string {
	return "Authentication error"
}


func NewWebSocketServerTransport(w http.ResponseWriter, r *http.Request) (Transport, error) {
        return newCoderWebsocketServerTransport(w, r)	
        //return newGobwasServerTransport(w, r)	
}

func NewWebSocketClientTransport(ctx context.Context, uri string) (Transport, error) {
        return newCoderWebsocketClientTransport(ctx, uri)
        //return newGobwasClientTransport(ctx, uri)
}
