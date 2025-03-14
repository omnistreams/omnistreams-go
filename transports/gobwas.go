package transports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/gobwas/ws"
)

type gobwasTransport struct {
	sentClose bool
	isServer  bool
	gsConn    net.Conn
	muRead    *sync.Mutex
	muWrite   *sync.Mutex
}

func newGobwasServerTransport(w http.ResponseWriter, r *http.Request) (*gobwasTransport, error) {
        gsConn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		return nil, err
	}

	return newGobwasTransport(gsConn, true), nil
}


func newGobwasClientTransport(ctx context.Context, uri string) (*gobwasTransport, error) {
        gsConn, br, _, err := ws.Dial(ctx, uri)
	if err != nil {
		return nil, err
	}

	if br != nil {
		return nil, errors.New("newGobwasTransport: Non nil br")
	}

	return newGobwasTransport(gsConn, false), nil
}

func newGobwasTransport(gsConn net.Conn, isServer bool) *gobwasTransport {
	return &gobwasTransport{
		isServer: isServer,
		gsConn:   gsConn,
		muRead:   &sync.Mutex{},
		muWrite:  &sync.Mutex{},
	}
}

func (wr *gobwasTransport) Read(ctx context.Context) ([]byte, error) {

	wr.muRead.Lock()
	defer wr.muRead.Unlock()

	header, err := ws.ReadHeader(wr.gsConn)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, header.Length)
	n, err := io.ReadFull(wr.gsConn, buf)
	if err != nil {
		return nil, err
	}

	if int64(n) != header.Length {
		return nil, errors.New("Didn't read enough")
	}

	if header.Masked {
		ws.Cipher(buf, header.Mask, 0)
	}

	if header.OpCode != ws.OpBinary && header.OpCode != ws.OpClose {
		return nil, fmt.Errorf("Unhandled OpCode %v", header.OpCode)
	}

	if header.OpCode == ws.OpClose {

		alreadySent := false

		wr.muWrite.Lock()
		isServer := wr.isServer
		if wr.sentClose {
			alreadySent = true
		} else {
			wr.sentClose = true
		}
		wr.muWrite.Unlock()

		if !alreadySent {
			err = wr.sendClose(ctx)
			if err != nil {
				return nil, err
			}
		} else if isServer {
			err := wr.gsConn.Close()
			if err != nil {
				return nil, err
			}
		}

		return nil, errors.New("dun closed")
	}

	return buf, nil
}

func (wr *gobwasTransport) Write(ctx context.Context, msg []byte) error {

	wr.muWrite.Lock()
	defer wr.muWrite.Unlock()

	frame := ws.NewBinaryFrame(msg)

	// We're doing this as two separate calls rather than using
	// ws.WriteFrame because that function doesn't let you verify how many
	// bytes were sent.
	err := ws.WriteHeader(wr.gsConn, frame.Header)
	if err != nil {
		return err
	}

	n, err := wr.gsConn.Write(frame.Payload)
	if err != nil {
		return err
	}

	if n != len(msg) {
		return errors.New("Didn't send the correct number of bytes")
	}

	return nil
}

func (wr *gobwasTransport) sendClose(ctx context.Context) error {

	wr.muWrite.Lock()
	defer wr.muWrite.Unlock()

	n, err := wr.gsConn.Write(ws.CompiledClose)
	if err != nil {
		return err
	}

	if n != len(ws.CompiledClose) {
		return errors.New("sendClose: didn't write correct amount")
	}

	return nil
}
