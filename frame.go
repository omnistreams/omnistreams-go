package omnistreams

import (
	"encoding/binary"
	"fmt"
	"strconv"
)

const HeaderSize = 8

type FrameType uint8

const (
	FrameTypeReset = iota
	FrameTypeData
	FrameTypeWindowIncrease
	FrameTypeGoAway // TODO: change to ConnectionClose (QUIC)
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
