package transports

import (
	"strings"
        "errors"
	"context"
	"net/http"
        "sync"

	"github.com/coder/websocket"
)


type coderWebsocketTransport struct {
        wsConn *websocket.Conn
        // TODO: why are mutexes necessary?
	muRead    *sync.Mutex
	muWrite   *sync.Mutex
}

func newCoderWebsocketServerTransport(w http.ResponseWriter, r *http.Request) (*coderWebsocketTransport, error) {

        wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
                OriginPatterns: []string{"*"},
        })
        if err != nil {
                return nil, err
        }

        wsConn.SetReadLimit(128*1024)

        return &coderWebsocketTransport{
                wsConn: wsConn,
		muRead:   &sync.Mutex{},
		muWrite:  &sync.Mutex{},
        }, nil
}

func newCoderWebsocketClientTransport(ctx context.Context, uri string) (*coderWebsocketTransport, error) {
	wsConn, _, err := websocket.Dial(ctx, uri, nil)
        if err != nil {

		// TODO: so hacky. Depends on implementation details of
		// websocket library. I opened an issue here:
		// https://github.com/coder/websocket/issues/528
		if strings.Contains(err.Error(), "401") {
			return nil, &AuthenticationError{}
		}

                return nil, err
        }

        wsConn.SetReadLimit(128*1024)

        return &coderWebsocketTransport{
                wsConn: wsConn,
		muRead:   &sync.Mutex{},
		muWrite:  &sync.Mutex{},
        }, nil
}

func (ts *coderWebsocketTransport) Read(ctx context.Context) ([]byte, error) {

	ts.muRead.Lock()
	defer ts.muRead.Unlock()

        msgType, msg, err := ts.wsConn.Read(ctx)
        if err != nil {
                return nil, err
        }

        if msgType != websocket.MessageBinary {
                return nil, errors.New("Message type must be binary")
        }

        return msg, nil
}

func (ts *coderWebsocketTransport) Write(ctx context.Context, msg []byte) error {
	ts.muWrite.Lock()
	defer ts.muWrite.Unlock()

        return ts.wsConn.Write(ctx, websocket.MessageBinary, msg)
}
