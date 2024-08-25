package omnistreams

import (
	"context"
	"errors"
	"log"
	"sync"
)

type Transport interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
}

type Connection struct {
	nextStreamId       uint32
	streams            map[uint32]*Stream
	streamCh           chan *Stream
	mut                *sync.Mutex
	transport          Transport
	recvWindowUpdateCh chan windowUpdateEvent
	eventCh            chan Event
	wg                 *sync.WaitGroup
}

type Event interface{}
type StreamCreatedEvent struct{}
type StreamDeletedEvent struct{}
type DebugEvent int

func NewConnection(transport Transport, isClient bool) *Connection {

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
		transport:          transport,
		recvWindowUpdateCh: recvWindowUpdateCh,
		wg:                 &sync.WaitGroup{},
	}

	go func() {

		ctx := context.Background()

		for {
			msgBytes, err := transport.Read(ctx)
			if err != nil {
				log.Println("Error transport.Read", err)
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

		// TODO: not sure why this lock is here. Seems dangerous
		mut.Lock()
		defer mut.Unlock()

		for _, stream := range c.streams {
			err := stream.Close()
			if err != nil {
				log.Println(err)
			}
		}

		close(c.streamCh)

		if c.eventCh != nil {
			close(c.eventCh)
		}
		c.eventCh = nil

		go func() {
			// Wait until all streams are done to close this channel
			c.wg.Wait()
			close(recvWindowUpdateCh)
		}()
	}()

	go func() {

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
	}()

	return c
}

func (c *Connection) Events() chan Event {
	eventCh := make(chan Event, 1)
	c.eventCh = eventCh
	return eventCh
}

func (c *Connection) AcceptStream() (*Stream, error) {
	strm, ok := <-c.streamCh
	if !ok {
		return nil, errors.New("Connection closed")
	}
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
	//c.mut.Lock()
	//fmt.Println("Send frame")
	//fmt.Println(frame)
	//c.mut.Unlock()

	packedFrame := packFrame(frame)
	err := c.transport.Write(context.Background(), packedFrame)
	if err != nil {
		return err
	}

	return nil
}

func (c *Connection) handleFrame(f *frame) {

	//c.mut.Lock()
	//fmt.Println("Receive frame")
	//fmt.Printf("%+v\n", f)
	//c.mut.Unlock()

	c.mut.Lock()
	stream, streamExists := c.streams[f.streamId]
	c.mut.Unlock()

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

			//Dash.Set("stream-queue-len", 1)

			c.streamCh <- stream
		}

		stream.recvCh <- f.data

		if f.fin {
			close(stream.recvCh)
			close(stream.remoteCloseCh)
			// TODO: make sure streams aren't leaking. Removed this because it was preventing
			// window increases from being received on half-closed streams
			//delete(c.streams, f.streamId)
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
			// TODO: this might be leaking now
			//c.mut.Lock()
			//delete(c.streams, f.streamId)
			//c.mut.Unlock()

			err := stream.Close()
			if err != nil {
				log.Println("FrameTypeReset:", err)
			}
		}
	case FrameTypeGoAway:

		log.Println("FrameTypeGoAway:", f.errorCode)

		close(c.streamCh)

		// TODO: clean up all streams

	default:
		log.Println("Frame type not implemented:", f.frameType)
	}
}

func (c *Connection) newStream(streamId uint32, syn bool) *Stream {

	c.wg.Add(1)

	sendCh := make(chan []byte)

	stream := NewStream(streamId, sendCh, c.recvWindowUpdateCh)

	readClosed := false
	writeClosed := false
	checkClosed := func() {
		if readClosed && writeClosed {
			c.mut.Lock()
			delete(c.streams, streamId)
			c.mut.Unlock()

			c.wg.Done()

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
					log.Println("<-sendCh:", err)
					break LOOP
				}
			case <-stream.closeWriteCh:
				err := c.sendFrame(&frame{
					frameType: FrameTypeData,
					streamId:  streamId,
					syn:       false,
					fin:       true,
				})
				if err != nil {
					log.Println("<-stream.closeWriteCh:", err)
				}

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
			err := c.sendFrame(&frame{
				frameType: FrameTypeReset,
				streamId:  streamId,
				errorCode: 42,
			})
			if err != nil {
				log.Println("<-stream.closeReadCh:", err)
			}
		}

		readClosed = true
		checkClosed()
	}()

	c.mut.Lock()
	c.streams[streamId] = stream
	c.mut.Unlock()

	if c.eventCh != nil {
		c.eventCh <- &StreamCreatedEvent{}
	}

	return stream
}
