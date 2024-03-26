package omnistreams

import (
	"context"
	"fmt"
	"sync"

	"nhooyr.io/websocket"
)

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
