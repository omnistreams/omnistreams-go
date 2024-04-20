package omnistreams

import (
	"context"
	"fmt"
	"sync"
)

type ChunkStream interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
}

type Connection struct {
	nextStreamId  uint32
	streams       map[uint32]*Stream
	streamCh      chan *Stream
	mut           *sync.Mutex
	chunkStream   ChunkStream
	messageStream *Stream
}

func NewConnection(chunkStream ChunkStream, isClient bool) *Connection {

	streams := make(map[uint32]*Stream)
	mut := &sync.Mutex{}
	streamCh := make(chan *Stream)

	// TODO: Once we break compatibility with muxado, maybe switch this to
	// match WebTransport, which uses even numbered stream IDs for clients
	var nextStreamId uint32 = 2
	if isClient {
		nextStreamId = 1
	}

	c := &Connection{
		nextStreamId: nextStreamId,
		streams:      streams,
		streamCh:     streamCh,
		mut:          mut,
		chunkStream:  chunkStream,
	}

	messageStream := c.newStream(0, false)

	c.messageStream = messageStream

	streams[0] = messageStream

	go func() {

		ctx := context.Background()

		for {
			msgBytes, err := chunkStream.Read(ctx)
			if err != nil {
				fmt.Println("Error chunkStream.Read", err)
				break
			}

			//if msgType != websocket.MessageBinary {
			//	fmt.Println("Invalid message type")
			//	break
			//}

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
	return c.messageStream.ReadMessage()
}

func (c *Connection) SendMessage(msg []byte) error {
	_, err := c.messageStream.Write(msg)
	return err
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

	stream := c.newStream(streamId, true)

	return stream, nil
}

func (c *Connection) sendFrame(frame *frame) error {
	//fmt.Println("Send frame")
	//fmt.Println(frame)

	packedFrame := packFrame(frame)
	err := c.chunkStream.Write(context.Background(), packedFrame)
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
			stream = c.newStream(f.streamId, false)

			c.streamCh <- stream
		}

		stream.recvCh <- f.data

		if f.fin {
			close(stream.recvCh)
			//fmt.Println("here50")
			//err := stream.CloseRead()
			//if err != nil {
			//	fmt.Println("stream.CloseRead()", err)
			//}

			// TODO: delete stream at some point. I don't think
			// this is the right way to do it because I think it would
			// be a race condition to flush the OS TCP queues.
			//c.mut.Lock()
			//delete(c.streams, f.streamId)
			//c.mut.Unlock()
		}

		if len(f.data) > 0 {
			// TODO: should maybe be doing this in a goroutine
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

func (c *Connection) newStream(streamId uint32, syn bool) *Stream {

	sendCh := make(chan []byte)

	stream := NewStream(streamId, sendCh)

	// TODO: I think these goroutines can be combined somewhat. Also need
	// to make sure they don't leak

	go func() {
	LOOP:
		for {
			select {
			case msg := <-sendCh:

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
			case <-stream.closeWriteCh:
				c.sendFrame(&frame{
					frameType: FrameTypeData,
					streamId:  streamId,
					syn:       false,
					fin:       true,
				})
				break LOOP
			}
		}
	}()

	go func() {
		// TODO: might not want to send if it was closed by the remote side
		<-stream.closeReadCh
		c.sendFrame(&frame{
			frameType: FrameTypeReset,
			streamId:  streamId,
			errorCode: 42,
		})
	}()

	c.mut.Lock()
	c.streams[streamId] = stream
	c.mut.Unlock()

	return stream
}
