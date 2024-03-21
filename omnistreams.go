package omnistreams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"nhooyr.io/websocket"
)

const HeaderSize = 8

const (
	FrameTypeReset = iota
	FrameTypeData
	FrameTypeWindowIncrease
)

type Connection struct {
	nextStreamId uint32
	streams      map[uint32]*Stream
	streamCh     chan *Stream
	mut          *sync.Mutex
	wsConn       *websocket.Conn
}

func NewConnection(wsConn *websocket.Conn) *Connection {

	streams := make(map[uint32]*Stream)
	mut := &sync.Mutex{}
	streamCh := make(chan *Stream)

	c := &Connection{
		nextStreamId: 1,
		streams:      streams,
		streamCh:     streamCh,
		mut:          mut,
		wsConn:       wsConn,
	}

	go func() {

		ctx := context.Background()

		for {
			fmt.Println("loop msg")
			msgType, msgBytes, err := wsConn.Read(ctx)
			if err != nil {
				fmt.Println("Error wsConn.Read", err)
				break
			}

			if msgType != websocket.MessageBinary {
				fmt.Println("Invalid message type")
				break
			}

			frame, err := unpackFrame(msgBytes)
			if err != nil {
				fmt.Println("Error unpackFrame", err)
				break
			}

			fmt.Println(len(frame.data))
			//fmt.Printf("%+v\n", frame)

			c.handleFrame(frame)
		}
	}()

	return c
}

func (c *Connection) AcceptStream() (*Stream, error) {
	strm := <-c.streamCh
	return strm, nil
}

func (c *Connection) OpenStream() (*Stream, error) {

	streamId := c.nextStreamId

	frm := &frame{
		frameType: FrameTypeData,
		streamId:  streamId,
		syn:       true,
	}

	err := c.sendFrame(frm)
	if err != nil {
		return nil, err
	}

	sendCh := make(chan []byte)

	go func() {
		for {
			msg := <-sendCh
			frm := &frame{
				frameType: FrameTypeData,
				streamId:  streamId,
				syn:       false,
				data:      msg,
			}

			err := c.sendFrame(frm)
			if err != nil {
				fmt.Println("TODO:", err)
			}
		}
	}()

	stream := &Stream{
		id:     streamId,
		recvCh: make(chan []byte),
		sendCh: sendCh,
	}

	c.mut.Lock()
	defer c.mut.Unlock()

	c.nextStreamId += 2
	c.streams[streamId] = stream

	return stream, nil
}

func (c *Connection) sendFrame(frame *frame) error {
	packedFrame := packFrame(frame)
	fmt.Println(packedFrame)
	err := c.wsConn.Write(context.Background(), websocket.MessageBinary, packedFrame)
	if err != nil {
		return err
	}

	return nil
}

func (c *Connection) handleFrame(f *frame) {
	c.mut.Lock()
	defer c.mut.Unlock()

	switch f.frameType {
	case FrameTypeData:
		stream, ok := c.streams[f.streamId]
		if !ok {
			sendCh := make(chan []byte)
			stream = &Stream{
				id:     f.streamId,
				recvCh: make(chan []byte),
				sendCh: sendCh,
			}

			go func() {
				for {
					msg := <-sendCh
					err := c.sendFrame(&frame{
						frameType: FrameTypeData,
						streamId:  stream.id,
						data:      msg,
					})

					if err != nil {
						fmt.Println(err)
					}
				}
			}()

			c.streamCh <- stream

			c.streams[f.streamId] = stream
		}

		stream.recvCh <- f.data

		err := c.sendFrame(&frame{
			frameType:      FrameTypeWindowIncrease,
			streamId:       stream.id,
			windowIncrease: uint32(len(f.data)),
		})

		if err != nil {
			fmt.Println(err)
		}

	default:
		fmt.Println("Frame type not implemented:", f.frameType)
	}
}

type Stream struct {
	id      uint32
	recvCh  chan []byte
	sendCh  chan []byte
	recvBuf []byte
}

func (s *Stream) StreamID() uint32 {
	return s.id
}

func (s *Stream) CloseWrite() error {
	return errors.New("CloseWrite not implemented")
}

func (s *Stream) Read(buf []byte) (int, error) {

	if s.recvBuf != nil {
		if len(s.recvBuf) > len(buf) {
			copy(buf, s.recvBuf[:len(buf)])
			s.recvBuf = s.recvBuf[len(buf):]
		} else {
			copy(buf, s.recvBuf)
			s.recvBuf = nil
		}

		return len(buf), nil
	}

	msg := <-s.recvCh

	if len(msg) > len(buf) {
		copy(buf, msg[:len(buf)])
		s.recvBuf = make([]byte, len(msg)-len(buf))
		copy(s.recvBuf, msg[len(buf):])
		return len(buf), nil
	}

	copy(buf, msg)
	return len(msg), nil
}

func (s *Stream) Write(msg []byte) (int, error) {

	buf := make([]byte, len(msg))

	copy(buf, msg)

	s.sendCh <- buf

	return len(msg), nil
}

func (s *Stream) Close() error {
	return errors.New("Not implemented")
}

func Upgrade(w http.ResponseWriter, r *http.Request) (*Connection, error) {

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return nil, err
	}

	s := NewConnection(wsConn)

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

type frameType uint8

type frame struct {
	frameType      frameType
	syn            bool
	fin            bool
	streamId       uint32
	data           []byte
	windowIncrease uint32
}

func packFrame(f *frame) []byte {

	var length uint32 = 0

	if f.frameType == FrameTypeWindowIncrease {
		length = 4
	}

	if f.data != nil {
		length = uint32(len(f.data))
	}

	synBit := 0
	if f.syn {
		synBit = 1
	}

	finBit := 0
	if f.fin {
		finBit = 1
	}

	flags := (synBit << 1) | finBit

	buf := make([]byte, HeaderSize+length)

	buf[0] = byte(length >> 16)
	buf[1] = byte(length >> 8)
	buf[2] = byte(length)
	buf[3] = byte(f.frameType<<4) | byte(flags)
	buf[4] = byte(f.streamId >> 24)
	buf[5] = byte(f.streamId >> 16)
	buf[6] = byte(f.streamId >> 8)
	buf[7] = byte(f.streamId)

	if f.frameType == FrameTypeWindowIncrease {
		buf[8] = byte(f.windowIncrease >> 24)
		buf[9] = byte(f.windowIncrease >> 16)
		buf[10] = byte(f.windowIncrease >> 8)
		buf[11] = byte(f.windowIncrease)
	}

	copy(buf[HeaderSize:], f.data)

	return buf
}

func unpackFrame(packedFrame []byte) (*frame, error) {

	fa := packedFrame
	//length := (fa[0] << 16) | (fa[1] << 8) | (fa[2])
	ft := (fa[3] & 0b11110000) >> 4
	flags := (fa[3] & 0b00001111)
	fin := false
	if (flags & 0b0001) != 0 {
		fin = true
	}
	syn := false
	if (flags & 0b0010) != 0 {
		syn = true
	}
	streamId := uint32((fa[4] << 24) | (fa[5] << 16) | (fa[6] << 8) | fa[7])

	return &frame{
		frameType: frameType(ft),
		syn:       syn,
		fin:       fin,
		streamId:  streamId,
		data:      packedFrame[HeaderSize:],
	}, nil
}

func printJson(data interface{}) {
	d, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(d))
}
