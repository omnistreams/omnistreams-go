package omnistreams

import (
	"context"
	"log"
	"sync"
)

type ChunkStream interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
}

type Connection struct {
	nextStreamId       uint32
	streams            map[uint32]*Stream
	streamCh           chan *Stream
	mut                *sync.Mutex
	chunkStream        ChunkStream
	messageStream      *Stream
	recvWindowUpdateCh chan windowUpdateEvent
	eventCh            chan Event
}

type Event interface{}
type StreamCreatedEvent struct{}
type StreamDeletedEvent struct{}
type DebugEvent int

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

	recvWindowUpdateCh := make(chan windowUpdateEvent)

	c := &Connection{
		nextStreamId:       nextStreamId,
		streams:            streams,
		streamCh:           streamCh,
		mut:                mut,
		chunkStream:        chunkStream,
		recvWindowUpdateCh: recvWindowUpdateCh,
	}

	messageStream := c.newStream(0, false)

	c.messageStream = messageStream

	streams[0] = messageStream

	go func() {

		ctx := context.Background()

		for {
			msgBytes, err := chunkStream.Read(ctx)
			if err != nil {
				log.Println("Error chunkStream.Read", err)
				break
			}

			//if msgType != websocket.MessageBinary {
			//	fmt.Println("Invalid message type")
			//	break
			//}

			frame, err := unpackFrame(msgBytes)
			if err != nil {
				log.Println("Error unpackFrame", err)
				break
			}

			c.handleFrame(frame)
		}

		mut.Lock()
		defer mut.Unlock()

		err := messageStream.Close()
		if err != nil {
			log.Println(err)
		}
		close(recvWindowUpdateCh)
		close(c.eventCh)
		c.eventCh = nil
	}()

	go func() {
		if c.eventCh != nil {
			c.eventCh <- DebugEvent(1)
		}

		for evt := range recvWindowUpdateCh {
			err := c.sendFrame(&frame{
				frameType:      FrameTypeWindowIncrease,
				streamId:       evt.streamId,
				windowIncrease: evt.windowUpdate,
			})

			if err != nil {
				log.Println(err)
			}
		}

		if c.eventCh != nil {
			c.eventCh <- DebugEvent(-1)
		}
	}()

	return c
}

func (c *Connection) Events() chan Event {
	eventCh := make(chan Event, 1)
	c.eventCh = eventCh
	return eventCh
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
	stream := c.newStream(streamId, true)

	c.mut.Unlock()

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

	stream, streamExists := c.streams[f.streamId]

	switch f.frameType {
	case FrameTypeMessage:
		fallthrough
	case FrameTypeData:

		if !streamExists {
			// If syn isn't set this is likely a packet for a recently RST stream; ignore the frame
			// TODO: might want to have more advanced logic. Maybe keeping streams unavailable for
			// a while so we can specifically detect send on RST stream conditions
			if !f.syn {
				break
			}

			stream = c.newStream(f.streamId, false)

			c.streamCh <- stream
		}

		stream.recvCh <- f.data

		if f.fin {
			close(stream.recvCh)
			close(stream.remoteCloseCh)
			delete(c.streams, f.streamId)
		}

	case FrameTypeWindowIncrease:
		if !streamExists {
			log.Println("FrameTypeWindowIncrease: no such stream", f.streamId)
		} else {
			stream.updateWindow(f.windowIncrease)
		}
	case FrameTypeReset:

		log.Println("FrameTypeReset:", f.errorCode)

		if !streamExists {
			log.Println("FrameTypeReset: no such stream", f.streamId)
		} else {
			delete(c.streams, f.streamId)
			err := stream.Close()
			if err != nil {
				log.Println("FrameTypeReset:", err)
			}
		}

	default:
		log.Println("Frame type not implemented:", f.frameType)
	}
}

func (c *Connection) newStream(streamId uint32, syn bool) *Stream {

	sendCh := make(chan []byte)

	stream := NewStream(streamId, sendCh, c.recvWindowUpdateCh)

	readClosed := false
	writeClosed := false
	checkClosed := func() {
		if readClosed && writeClosed {
			c.mut.Lock()
			delete(c.streams, streamId)
			c.mut.Unlock()

			if c.eventCh != nil {
				c.eventCh <- &StreamDeletedEvent{}
			}
		}

	}

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
					log.Println("TODO:", err)
				}
			case <-stream.closeWriteCh:
				c.sendFrame(&frame{
					frameType: FrameTypeData,
					streamId:  streamId,
					syn:       false,
					fin:       true,
				})

				writeClosed = true
				checkClosed()

				break LOOP
			}
		}
	}()

	go func() {
		select {
		case <-stream.remoteCloseCh:
		case <-stream.closeReadCh:
			c.sendFrame(&frame{
				frameType: FrameTypeReset,
				streamId:  streamId,
				errorCode: 42,
			})
		}

		readClosed = true
		checkClosed()
	}()

	c.streams[streamId] = stream

	if c.eventCh != nil {
		c.eventCh <- &StreamCreatedEvent{}
	}

	return stream
}
