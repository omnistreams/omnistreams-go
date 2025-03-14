package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"

	"github.com/caddyserver/certmagic"
	//"github.com/quic-go/quic-go"
	"github.com/coder/websocket"
	"github.com/omnistreams/omnistreams-go"
	"github.com/omnistreams/omnistreams-go/transports"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	TestTypeConsume = iota
	TestTypeEcho
	TestTypeMimic
	TestTypeSend
)

const TestTypeSize = 1
const TestIdSize = 4
const TestHeaderSize = TestTypeSize + TestIdSize

type road interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
}

type session interface {
	OpenRoad() (road, error)
	AcceptRoad() (road, error)
}

type wsTransport struct {
	wsConn *websocket.Conn
}

func NewWsTransport(w http.ResponseWriter, r *http.Request) (*wsTransport, error) {

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return nil, err
	}

	wsConn.SetReadLimit(128 * 1024)

	return &wsTransport{
		wsConn,
	}, nil
}

func (wr *wsTransport) Read(ctx context.Context) ([]byte, error) {

	msgType, msgBytes, err := wr.wsConn.Read(ctx)
	if err != nil {
		return nil, err
	}

	if msgType != websocket.MessageBinary {
		return nil, errors.New("Invalid WS message type")
	}

	return msgBytes, nil
}

func (wr *wsTransport) Write(ctx context.Context, msg []byte) error {
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

	//n := runtime.SetMutexProfileFraction(1)
	//fmt.Println(n)
	runtime.SetBlockProfileRate(1)

	useTlsArg := flag.Bool("use-tls", false, "Use TLS")
	flag.Parse()

	useTls := *useTlsArg

	//mux := http.NewServeMux()
	mux := http.DefaultServeMux
	var wtServer *webtransport.Server

	var listener net.Listener
	var err error
	if useTls {
		listener, err = net.Listen("tcp", ":443")
		exitOnError(err)
	} else {
		listener, err = net.Listen("tcp", ":3000")
		exitOnError(err)
	}

	if useTls {
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

		listener = tls.NewListener(listener, tlsConfig)

		wtServer = &webtransport.Server{
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
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		var sess session

		if r.ProtoMajor == 3 {
			if wtServer == nil {
				fmt.Println("HTTP/3 not supported")
				return
			}

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

			transport, err := transports.NewWebSocketServerTransport(w, r)
			//transport, err := NewWsTransport(w, r)
			if err != nil {
				fmt.Println(err)
				return
			}

			osConn := omnistreams.NewConnection(transport, false)

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
	err = http.Serve(listener, mux)
	exitOnError(err)
}

func handleStream(conn session, stream road) {

	buf := make([]byte, 4096)

	nFirstChunkInt, err := stream.Read(buf)
	if err != nil {
		fmt.Println(err)
	}

	nFirstChunk := int64(nFirstChunkInt)

	testTypeByte := buf[0]

	switch testTypeByte {
	case TestTypeConsume:
		fmt.Println("TestTypeConsume")
		n, err := io.Copy(ioutil.Discard, stream)
		if err != nil {
			fmt.Println(err)
		}
		nFirstChunkPayload := nFirstChunk - TestHeaderSize
		fmt.Println("Consumed", n+nFirstChunkPayload)
	case TestTypeEcho:
		fmt.Println("TestTypeEcho")
		m, err := stream.Write(buf[:nFirstChunk])
		if err != nil {
			fmt.Println(err)
		}
		n, err := io.Copy(stream, stream)
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println("Echoed", int64(m)-TestHeaderSize+n)
	case TestTypeMimic:
		fmt.Println("TestTypeMimic")
		resStream, err := conn.OpenRoad()
		if err != nil {
			fmt.Println(err)
		}

		m, err := resStream.Write(buf[:nFirstChunk])
		if err != nil {
			fmt.Println(err)
		}
		n, err := io.Copy(resStream, stream)
		if err != nil {
			fmt.Println(err)
		}
		fmt.Println("Mimic'd", int64(m)-TestHeaderSize+n)
	case TestTypeSend:

		size := binary.BigEndian.Uint32(buf[TestHeaderSize:])

		var chunkSize uint32 = 256 * 1024

		totalSent := 0

		if size < chunkSize {
			chunk := make([]byte, size)

			n, err := stream.Write(chunk)
			if err != nil {
				fmt.Println(err)
				return
			}

			totalSent += n

		} else {
			chunk := make([]byte, chunkSize)

			var i uint32
			for i = 0; i < size; i += chunkSize {
				n, err := stream.Write(chunk)
				if err != nil {
					fmt.Println(err)
					return
				}

				totalSent += n
			}
		}

		fmt.Println("Sent", totalSent)
	default:
		fmt.Println("Unknown test type", testTypeByte)
	}
}

func exitOnError(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, err.Error())
		os.Exit(1)
	}
}
