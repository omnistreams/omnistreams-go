package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"

	"github.com/caddyserver/certmagic"
	"github.com/coder/websocket"
	"github.com/omnistreams/omnistreams-go"
)

const (
	TestTypeConsume = iota
	TestTypeEcho
	TestTypeMimic
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

	certmagic.DefaultACME.DisableHTTPChallenge = true
	certmagic.DefaultACME.Agreed = true
	//certmagic.DefaultACME.CA = certmagic.LetsEncryptStagingCA

	certConfig := certmagic.NewDefault()

	ctx := context.Background()
	err := certConfig.ManageSync(ctx, []string{"os.anderspitman.com"})
	exitOnError(err)

	tlsConfig := &tls.Config{
		GetCertificate: certConfig.GetCertificate,
		//NextProtos: []string{"http/1.1", "acme-tls/1"},
	}

	listener, err := net.Listen("tcp", ":443")
	exitOnError(err)

	tlsListener := tls.NewListener(listener, tlsConfig)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			OriginPatterns: []string{"*"},
		})
		if err != nil {
			fmt.Println(err)
			return
		}

		wsConn.SetReadLimit(128 * 1024)

		wr := NewWsConnWrapper(wsConn)

		conn := omnistreams.NewConnection(wr, false)

		for {
			stream, err := conn.AcceptStream()
			if err != nil {
				fmt.Println(err)
				break
			}

			go handleStream(conn, stream)
		}
	})

	fmt.Println("Running")
	err = http.Serve(tlsListener, nil)
	exitOnError(err)
}

func handleStream(conn *omnistreams.Connection, stream *omnistreams.Stream) {

	testTypeByte := []byte{0}

	_, err := stream.Read(testTypeByte)
	if err != nil {
		fmt.Println(err)
	}

	switch testTypeByte[0] {
	case TestTypeConsume:
		fmt.Println("TestTypeConsume")
		n, err := io.Copy(ioutil.Discard, stream)
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println("Consumed", n)
	case TestTypeEcho:
		fmt.Println("TestTypeEcho")
		_, err = stream.Write(testTypeByte)
		if err != nil {
			fmt.Println(err)
		}
		n, err := io.Copy(stream, stream)
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println("Echoed", n)
	case TestTypeMimic:
		fmt.Println("TestTypeMimic")
		resStream, err := conn.OpenStream()
		if err != nil {
			fmt.Println(err)
		}

		_, err = resStream.Write(testTypeByte)
		if err != nil {
			fmt.Println(err)
		}
		n, err := io.Copy(resStream, stream)
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println("Mimic'd", n)
	default:
		fmt.Println("Unknown test type", testTypeByte[0])
	}
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
}
