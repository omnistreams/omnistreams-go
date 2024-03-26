package omnistreams

import (
	"errors"
	"fmt"
	"sync/atomic"
)

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
