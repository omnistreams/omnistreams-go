package omnistreams

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"nhooyr.io/websocket"
)

const HeaderSize = 8

type FrameType uint8

const (
	FrameTypeReset = iota
	FrameTypeData
	FrameTypeWindowIncrease
	FrameTypeGoAway
	FrameTypeMessage
)

func (ft FrameType) String() string {
	switch ft {
	case FrameTypeReset:
		return "FrameTypeReset"
	case FrameTypeData:
		return "FrameTypeData"
	case FrameTypeWindowIncrease:
		return "FrameTypeWindowIncrease"
	case FrameTypeGoAway:
		return "FrameTypeGoAway"
	default:
		return fmt.Sprintf("Unknown frame type: %d", ft)
	}
}

type Connection struct {
	nextStreamId   uint32
	streams        map[uint32]*Stream
	streamCh       chan *Stream
	mut            *sync.Mutex
	wsConn         *websocket.Conn
	datagramStream *Stream
}

func NewConnection(wsConn *websocket.Conn, isClient bool) *Connection {

	streams := make(map[uint32]*Stream)
	mut := &sync.Mutex{}
	streamCh := make(chan *Stream)

	// TODO: Once we break compatibility with muxado, maybe switch this to
	// match WebTransport, which uses even numbered stream IDs for clients
	var nextStreamId uint32 = 2
	if isClient {
		nextStreamId = 1
	}

	datagramSendCh := make(chan []byte)

	datagramStream := NewStream(0, datagramSendCh)

	streams[0] = datagramStream

	c := &Connection{
		nextStreamId:   nextStreamId,
		streams:        streams,
		streamCh:       streamCh,
		mut:            mut,
		wsConn:         wsConn,
		datagramStream: datagramStream,
	}

	go func() {

		for {
			msg := <-datagramSendCh

			frm := &frame{
				frameType: FrameTypeMessage,
				data:      msg,
			}

			err := c.sendFrame(frm)
			if err != nil {
				fmt.Println("TODO dgram send:", err)
			}
		}
	}()

	go func() {

		ctx := context.Background()

		for {
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

			go c.handleFrame(frame)
		}
	}()

	return c
}

func (c *Connection) ReceiveMessage() ([]byte, error) {
	return c.datagramStream.ReadMessage()
}

func (c *Connection) SendMessage(msg []byte) error {
	return c.datagramStream.WriteMessage(msg)
}

func (c *Connection) AcceptStream() (*Stream, error) {
	strm := <-c.streamCh
	return strm, nil
}

func (c *Connection) OpenStream() (*Stream, error) {

	c.mut.Lock()
	streamId := c.nextStreamId
	c.nextStreamId += 2
	c.mut.Unlock()

	sendCh := make(chan []byte)

	go func() {

		syn := true
		for {
			msg := <-sendCh

			frm := &frame{
				frameType: FrameTypeData,
				streamId:  streamId,
				syn:       syn,
				data:      msg,
			}

			if syn {
				syn = false
			}

			err := c.sendFrame(frm)
			if err != nil {
				fmt.Println("TODO:", err)
			}
		}
	}()

	stream := NewStream(streamId, sendCh)

	go func() {
		// TODO: might not want to send if it was closed by the remote side
		<-stream.closeReadCh
		c.sendFrame(&frame{
			frameType: FrameTypeReset,
			streamId:  streamId,
			errorCode: 42,
		})
	}()

	go func() {
		<-stream.closeWriteCh
		c.sendFrame(&frame{
			frameType: FrameTypeData,
			streamId:  streamId,
			syn:       false,
			fin:       true,
		})
	}()

	c.mut.Lock()
	c.streams[streamId] = stream
	c.mut.Unlock()

	return stream, nil
}

func (c *Connection) sendFrame(frame *frame) error {
	//fmt.Println("Send frame")
	//fmt.Println(frame)

	packedFrame := packFrame(frame)
	err := c.wsConn.Write(context.Background(), websocket.MessageBinary, packedFrame)
	if err != nil {
		return err
	}

	return nil
}

func (c *Connection) handleFrame(f *frame) {

	//fmt.Println("Receive frame")
	//fmt.Printf("%+v\n", f)

	switch f.frameType {
	case FrameTypeMessage:
		fallthrough
	case FrameTypeData:
		c.mut.Lock()
		stream, ok := c.streams[f.streamId]
		c.mut.Unlock()
		if !ok {
			sendCh := make(chan []byte)
			stream = NewStream(f.streamId, sendCh)

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

			c.mut.Lock()
			c.streams[f.streamId] = stream
			c.mut.Unlock()
		}

		stream.recvCh <- f.data

		if f.fin {
			err := stream.CloseRead()
			if err != nil {
				fmt.Println("stream.CloseRead()", err)
			}

			// TODO: delete stream at some point. I don't think
			// this is the right way to do it because I think it would
			// be a race condition to flush the OS TCP queues.
			//c.mut.Lock()
			//delete(c.streams, f.streamId)
			//c.mut.Unlock()
		}

		if len(f.data) > 0 {
			err := c.sendFrame(&frame{
				frameType:      FrameTypeWindowIncrease,
				streamId:       stream.id,
				windowIncrease: uint32(len(f.data)),
			})

			if err != nil {
				fmt.Println(err)
			}
		}

	case FrameTypeWindowIncrease:
		c.mut.Lock()
		stream, ok := c.streams[f.streamId]
		c.mut.Unlock()
		if !ok {
			fmt.Println("FrameTypeWindowIncrease: no such stream")
			return
		}

		stream.updateWindow(f.windowIncrease)
	case FrameTypeReset:

		fmt.Println("FrameTypeReset:", f.errorCode)

		c.mut.Lock()
		stream, ok := c.streams[f.streamId]
		c.mut.Unlock()

		if !ok {
			fmt.Println("FrameTypeReset: no such stream", f.streamId)
			return
		}

		err := stream.Close()
		if err != nil {
			fmt.Println("FrameTypeReset:", err)
		}
	default:
		fmt.Println("Frame type not implemented:", f.frameType)
	}
}

type Stream struct {
	id             uint32
	recvCh         chan []byte
	sendCh         chan []byte
	sendWindow     uint32
	windowUpdateCh chan uint32
	writeCh        chan []byte
	recvBuf        []byte
	closeReadCh    chan struct{}
	closeWriteCh   chan struct{}
	readClosed     atomic.Bool
	writeClosed    atomic.Bool
}

func NewStream(streamId uint32, sendCh chan []byte) *Stream {

	writeCh := make(chan []byte)

	stream := &Stream{
		id:             streamId,
		recvCh:         make(chan []byte),
		sendCh:         sendCh,
		windowUpdateCh: make(chan uint32),
		writeCh:        writeCh,
		sendWindow:     256 * 1024,
		closeReadCh:    make(chan struct{}),
		closeWriteCh:   make(chan struct{}),
	}

	go func() {
		for {
			msg := <-writeCh

			msgLen := uint32(len(msg))

			for {
				if stream.sendWindow >= msgLen {
					sendCh <- msg
					stream.sendWindow -= msgLen
					break
				} else {
					windowIncrease := <-stream.windowUpdateCh
					stream.sendWindow += windowIncrease
				}
			}
		}
	}()

	return stream
}

func (s *Stream) StreamID() uint32 {
	return s.id
}

func (s *Stream) Close() error {
	err := s.CloseRead()
	if err != nil {
		return err
	}
	return s.CloseWrite()
}

func (s *Stream) CloseRead() error {
	if !s.readClosed.Load() {
		s.readClosed.Store(true)
		close(s.closeReadCh)

		// Need to flush this stream because otherwise the OS buffers can
		// remain blocked and deadlock new streams due to TCP head-of-line
		// blocking
		go func() {
		LOOP:
			for {
				select {
				case <-s.recvCh:
				default:
					break LOOP
				}
			}
		}()
	}

	return nil
}

func (s *Stream) CloseWrite() error {
	if !s.writeClosed.Load() {
		s.writeClosed.Store(true)
		close(s.closeWriteCh)
	}
	return nil
}

func (s *Stream) ReadMessage() ([]byte, error) {
	select {
	case msg := <-s.recvCh:
		return msg, nil
	case _, ok := <-s.closeReadCh:
		if !ok {
			fmt.Println("ReadMessage: read closed, returning error")
			return nil, errors.New("Stream read closed")
		}
	}

	return nil, errors.New("ReadMessage failed for unknown reason")

}

func (s *Stream) WriteMessage(msg []byte) error {

	buf := make([]byte, len(msg))
	copy(buf, msg)

	select {
	case s.writeCh <- buf:
		return nil
	case _, ok := <-s.closeWriteCh:
		if !ok {
			fmt.Println("write closed, returning error")
			return errors.New("Stream write closed")
		}
	}

	return errors.New("WriteMessage failed for unknown reason")
}

func (s *Stream) Read(buf []byte) (int, error) {

	if s.recvBuf != nil {
		var n int
		if len(s.recvBuf) > len(buf) {
			n = copy(buf, s.recvBuf[:len(buf)])
			s.recvBuf = s.recvBuf[len(buf):]
		} else {
			n = copy(buf, s.recvBuf)
			s.recvBuf = nil
		}

		return n, nil
	}

	var msg []byte

	select {
	case msg = <-s.recvCh:
	case _, ok := <-s.closeReadCh:
		if !ok {
			fmt.Println("read closed, returning error")
			return 0, errors.New("Stream read closed")
		}
	}

	if len(msg) > len(buf) {
		copy(buf, msg[:len(buf)])
		s.recvBuf = make([]byte, len(msg)-len(buf))
		copy(s.recvBuf, msg[len(buf):])
		return len(buf), nil
	}

	copy(buf, msg)
	return len(msg), nil
}

func (s *Stream) Write(p []byte) (int, error) {

	buf := make([]byte, len(p))
	copy(buf, p)

	select {
	case s.writeCh <- buf:
	case _, ok := <-s.closeWriteCh:
		if !ok {
			fmt.Println("write closed, returning error")
			return 0, errors.New("Stream write closed")
		}
	}

	return len(buf), nil
}

func (s *Stream) updateWindow(windowUpdate uint32) {
	s.windowUpdateCh <- windowUpdate
}

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

type frame struct {
	frameType      FrameType
	syn            bool
	fin            bool
	streamId       uint32
	data           []byte
	windowIncrease uint32
	errorCode      uint32
}

func (f frame) String() string {
	s := "frame: {\n"

	s += fmt.Sprintf("  frameType: %s\n", f.frameType.String())
	s += fmt.Sprintf("  streamId: %d\n", +f.streamId)
	s += fmt.Sprintf("  syn: %t\n", f.syn)
	s += fmt.Sprintf("  fin: %t\n", f.fin)
	s += fmt.Sprintf("  len(data): %d\n", len(f.data))

	s += "  data[0:16]: [ "
	for i := 0; i < 16; i++ {
		if i >= len(f.data) {
			break
		}

		s += fmt.Sprintf("%d ", f.data[i])
	}
	s += "]\n"

	str := strconv.Quote(string(f.data))
	s += "  string(data): "
	beg := ""
	end := ""
	for i := 0; i < len(str); i++ {
		if i < 32 {
			beg += string(str[i])
		}
		if i > len(str)-32 {
			end += string(str[i])
		}
	}
	s += fmt.Sprintf("%s...%s\n", beg, end)

	s += "}"

	return s
}

func packFrame(f *frame) []byte {

	var length uint32 = 0

	if f.frameType == FrameTypeWindowIncrease || f.frameType == FrameTypeReset {
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

	binary.BigEndian.PutUint32(buf[4:8], f.streamId)

	switch f.frameType {
	case FrameTypeWindowIncrease:
		binary.BigEndian.PutUint32(buf[8:12], f.windowIncrease)
	case FrameTypeReset:
		binary.BigEndian.PutUint32(buf[8:12], f.errorCode)
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

	streamId := binary.BigEndian.Uint32(fa[4:8])

	frame := &frame{
		frameType: FrameType(ft),
		syn:       syn,
		fin:       fin,
		streamId:  streamId,
		data:      packedFrame[HeaderSize:],
	}

	switch frame.frameType {
	case FrameTypeWindowIncrease:
		data := packedFrame[HeaderSize:]
		frame.windowIncrease = binary.BigEndian.Uint32(data)
	case FrameTypeReset:
		data := packedFrame[HeaderSize:]
		frame.errorCode = binary.BigEndian.Uint32(data)
	case FrameTypeMessage:
		frame.streamId = 0
	}

	return frame, nil
}

func printJson(data interface{}) {
	d, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(d))
}
