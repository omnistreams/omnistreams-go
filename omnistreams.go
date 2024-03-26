package omnistreams

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"nhooyr.io/websocket"
)

func Upgrade(w http.ResponseWriter, r *http.Request) (*Connection, error) {

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return nil, err
	}

	s := NewConnection(wsConn, false)

	return s, nil
}

type result struct {
	conn *Connection
	err  error
}

type Listener struct {
	connChan chan *result
}

func Listen() (*Listener, error) {

	connChan := make(chan *result)

	l := &Listener{
		connChan: connChan,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		conn, err := Upgrade(w, r)
		if err != nil {
			connChan <- &result{
				conn: nil,
				err:  err,
			}
			return
		}

		connChan <- &result{
			conn: conn,
			err:  nil,
		}
	})

	ln, err := net.Listen("tcp", ":3000")
	if err != nil {
		return nil, err
	}

	go func() {
		fmt.Println("Running")
		http.Serve(ln, mux)
	}()

	return l, nil
}

func (l *Listener) Accept() (*Connection, error) {
	result := <-l.connChan
	return result.conn, result.err
}

func printJson(data interface{}) {
	d, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(d))
}
