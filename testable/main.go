package main

import (
        "context"
        "errors"
        "fmt"
        "net/http"

        "github.com/coder/websocket"
        "github.com/omnistreams/omnistreams-go"
)

type wsConnWrapper struct {
	wsConn *websocket.Conn
}

func NewWsConnWrapper(wsConn *websocket.Conn) *wsConnWrapper {
	return &wsConnWrapper{
		wsConn,
	}
}

func (wr *wsConnWrapper) Read(ctx context.Context) ([]byte, error) {

	msgType, msgBytes, err := wr.wsConn.Read(ctx)
	if err != nil {
		return nil, err
	}

	if msgType != websocket.MessageBinary {
		return nil, errors.New("Invalid WS message type")
	}

	return msgBytes, nil
}

func (wr *wsConnWrapper) Write(ctx context.Context, msg []byte) error {
	return wr.wsConn.Write(ctx, websocket.MessageBinary, msg)
}

func main() {
        http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
                wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
                        OriginPatterns: []string{"*"},
                })
                if err != nil {
                        fmt.Println(err)
                        return
                }

                wr := NewWsConnWrapper(wsConn)

                conn := omnistreams.NewConnection(wr, false)

                fmt.Println(conn)

                //stream, err := conn.OpenStream()
                //if err != nil {
                //        fmt.Println(err)
                //}

                //fmt.Println(stream)
                //stream.Write([]byte("Hi there"))

                for {
                        stream, err := conn.AcceptStream()
                        if err != nil {
                                fmt.Println(err)
                                break
                        }

                        fmt.Println("got stream", stream)
                }
        })

        fmt.Println("Running")
        err := http.ListenAndServe(":3000", nil)
        fmt.Println(err)
}


