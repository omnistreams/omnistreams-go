package omnistreams

import (
	"errors"
	"io"
	"log"
	"sync/atomic"
)

type Stream struct {
	id             uint32
	recvCh         chan []byte
	sendCh         chan []byte
	sendWindow     uint32
	windowUpdateCh chan uint32
	recvBuf        []byte
	closeReadCh    chan struct{}
	closeWriteCh   chan struct{}
	remoteCloseCh  chan struct{}
	readClosed     atomic.Bool
	writeClosed    atomic.Bool
	recvWindowCh   chan windowUpdateEvent
}

type windowUpdateEvent struct {
	streamId     uint32
	windowUpdate uint32
}

func NewStream(streamId uint32, sendCh chan []byte, recvWindowCh chan windowUpdateEvent) *Stream {

	stream := &Stream{
		id: streamId,
		// TODO: using a channel as a buffer here is pretty hacky. Instead we should never block sends
		// to recvCh and check against the receive window. If there's too much data coming in we
		// need to reset the connection because that means the sender is exceeding the window.
		recvCh:         make(chan []byte, 10),
		sendCh:         sendCh,
		windowUpdateCh: make(chan uint32, 1),
		sendWindow:     256 * 1024,
		closeReadCh:    make(chan struct{}),
		closeWriteCh:   make(chan struct{}),
		remoteCloseCh:  make(chan struct{}),
		recvWindowCh:   recvWindowCh,
	}

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
				case _, ok := <-s.recvCh:
					if !ok {
						break LOOP
					}
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
	case msg, ok := <-s.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case _, ok := <-s.closeReadCh:
		if !ok {
			log.Println("ReadMessage: read closed, returning error")
			return nil, errors.New("Stream read closed")
		}
	}

	return nil, errors.New("ReadMessage failed for unknown reason")

}

func (s *Stream) WriteMessage(msg []byte) error {
	return errors.New("WriteMessage not implemented")

	//buf := make([]byte, len(msg))
	//copy(buf, msg)

	//select {
	//case s.writeCh <- buf:
	//	return nil
	//case _, ok := <-s.closeWriteCh:
	//	if !ok {
	//		fmt.Println("write closed, returning error")
	//		return errors.New("Stream write closed")
	//	}
	//}

	//return errors.New("WriteMessage failed for unknown reason")
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

	var ok bool
	select {
	case msg, ok = <-s.recvCh:
		if !ok {
			return 0, io.EOF
		}
	case _, ok := <-s.closeReadCh:
		if !ok {
			log.Println("read closed, returning error")
			return 0, errors.New("Stream read closed")
		}
	}

	if len(msg) > 0 {
		s.recvWindowCh <- windowUpdateEvent{
			streamId:     s.id,
			windowUpdate: uint32(len(msg)),
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

	msgLen := uint32(len(buf))

	for {
		if s.sendWindow >= msgLen {
			s.sendCh <- buf
			s.sendWindow -= msgLen
			break
		} else {
			select {
			case windowIncrease := <-s.windowUpdateCh:
				s.sendWindow += windowIncrease
			case _, ok := <-s.closeWriteCh:
				if !ok {
					log.Println("write closed, returning error")
					return 0, errors.New("Stream write closed")
				}
			}
		}
	}

	return len(buf), nil
}

func (s *Stream) updateWindow(windowUpdate uint32) {
	s.windowUpdateCh <- windowUpdate
}
