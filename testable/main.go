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
	//"github.com/quic-go/quic-go"
	"github.com/coder/websocket"
	"github.com/omnistreams/omnistreams-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	TestTypeConsume = iota
	TestTypeEcho
	TestTypeMimic
)

type road interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
}

type session interface {
	OpenRoad() (road, error)
	AcceptRoad() (road, error)
}

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

type wtSessionWrapper struct {
	wtSession *webtransport.Session
}

func (s *wtSessionWrapper) OpenRoad() (road, error) {
	return s.wtSession.OpenStream()
}
func (s *wtSessionWrapper) AcceptRoad() (road, error) {
	ctx := context.Background()
	return s.wtSession.AcceptStream(ctx)
}

type osConnWrapper struct {
	osConn *omnistreams.Connection
}

func (c *osConnWrapper) OpenRoad() (road, error) {
	return c.osConn.OpenStream()
}
func (c *osConnWrapper) AcceptRoad() (road, error) {
	return c.osConn.AcceptStream()
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

	mux := http.NewServeMux()

	wtServer := webtransport.Server{
		H3: http3.Server{
			Addr:      ":5757",
			Handler:   mux,
			TLSConfig: tlsConfig,
			//QuicConfig: &quic.Config{
			//	//MaxIncomingStreams: 512,
			//	KeepAlivePeriod: 8,
			//},
		},
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	go wtServer.ListenAndServe()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		var sess session

		if r.ProtoMajor == 3 {
			wtSession, err := wtServer.Upgrade(w, r)
			if err != nil {
				w.WriteHeader(500)
				fmt.Println(err)
				return
			}

			wtSess := &wtSessionWrapper{
				wtSession,
			}

			sess = wtSess

		} else {
			wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				OriginPatterns: []string{"*"},
			})
			if err != nil {
				fmt.Println(err)
				return
			}

			wsConn.SetReadLimit(128 * 1024)

			wr := NewWsConnWrapper(wsConn)

			osConn := omnistreams.NewConnection(wr, false)

			osSess := &osConnWrapper{
				osConn,
			}

			sess = osSess
		}

		for {
			stream, err := sess.AcceptRoad()
			if err != nil {
				fmt.Println(err)
				break
			}

			go handleStream(sess, stream)
		}
	})

	fmt.Println("Running")
	err = http.Serve(tlsListener, mux)
	exitOnError(err)
}

func handleStream(conn session, stream road) {

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
		resStream, err := conn.OpenRoad()
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
