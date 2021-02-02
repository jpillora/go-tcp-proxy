package amqphelper

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"time"
)

// This file is based on github.com/streadway/amqp library
const (
	frameMethod    = 1
	frameHeader    = 2
	frameBody      = 3
	frameHeartbeat = 8
	frameEnd       = 206
)

// This is how amqp protocol works, frame is writer (of bytes)
type frame interface {
	write(io.Writer) error
	channel() uint16
}

type FrameReader struct {
	r io.Reader
	f frame
}

func NewFrameReader(r io.Reader) *FrameReader {
	return &FrameReader{
		r: r,
	}
}

func (r *FrameReader) Write(w io.Writer) error {
	return r.f.write(w)
}

func (r *FrameReader) UpdateMethodAckToNack() {
	mf, ok := r.f.(*methodFrame)
	if ok && mf != nil && mf.Method != nil {
		switch m := mf.Method.(type) {
		case *BasicAck:
			time.Sleep(time.Millisecond * time.Duration(rand.Intn(100)))
			mf.MethodId = 120
			mf.Method = &BasicNack{DeliveryTag: m.DeliveryTag, Multiple: m.Multiple}
		}
	}
}

// This struct will read from the io.Reader and return a frame
func (r *FrameReader) ReadFrame() (err error) {
	var scratch [7]byte

	if _, err = io.ReadFull(r.r, scratch[:7]); err != nil {
		return
	}

	typ := uint8(scratch[0])
	channel := binary.BigEndian.Uint16(scratch[1:3])
	size := binary.BigEndian.Uint32(scratch[3:7])

	switch typ {
	case frameMethod:
		if r.f, err = r.parseMethodFrame(channel, size); err != nil {
			return
		}

	case frameHeader:
		return errors.New("frameHeader, ignore")
		//if frame, err = r.parseHeaderFrame(channel, size); err != nil {
		//	return
		//}

	case frameBody:
		return errors.New("frameBody, ignore")
		//if frame, err = r.parseBodyFrame(channel, size); err != nil {
		//	return nil, err
		//}

	case frameHeartbeat:
		return errors.New("frameHeartbeat, ignore")
		//if frame, err = r.parseHeartbeatFrame(channel, size); err != nil {
		//	return
		//}

	default:
		return errors.New("frame could not be parsed")
	}

	if _, err = io.ReadFull(r.r, scratch[:1]); err != nil {
		return err
	}

	if scratch[0] != frameEnd {
		return errors.New("frameEnd fail")
	}

	return
}

func (r *FrameReader) parseMethodFrame(channel uint16, size uint32) (f frame, err error) {
	mf := &methodFrame{
		ChannelId: channel,
	}

	if err = binary.Read(r.r, binary.BigEndian, &mf.ClassId); err != nil {
		return
	}

	if err = binary.Read(r.r, binary.BigEndian, &mf.MethodId); err != nil {
		return
	}

	switch mf.ClassId {

	case 60: // basic
		switch mf.MethodId {

		case 80: // basic ack
			//fmt.Println("NextMethod: class:60 method:80")
			method := &BasicAck{}
			if err = method.read(r.r); err != nil {
				return
			}
			mf.Method = method

		default:
			return nil, fmt.Errorf("bad method frame, unknown method %d for class %d", mf.MethodId, mf.ClassId)
		}

	default:
		return nil, fmt.Errorf("bad method frame, unknown class %d", mf.ClassId)
	}

	return mf, nil
}

type message interface {
	id() (uint16, uint16)
	wait() bool
	read(io.Reader) error
	write(io.Writer) error
}

type methodFrame struct {
	ChannelId uint16
	ClassId   uint16
	MethodId  uint16
	Method    message
}

func (f *methodFrame) channel() uint16 { return f.ChannelId }

func (f *methodFrame) write(w io.Writer) (err error) {
	var payload bytes.Buffer

	if f.Method == nil {
		return errors.New("malformed frame: missing method")
	}

	class, method := f.Method.id()

	if err = binary.Write(&payload, binary.BigEndian, class); err != nil {
		return
	}

	if err = binary.Write(&payload, binary.BigEndian, method); err != nil {
		return
	}

	if err = f.Method.write(&payload); err != nil {
		return
	}

	return writeFrame(w, frameMethod, f.ChannelId, payload.Bytes())
}

func writeFrame(w io.Writer, typ uint8, channel uint16, payload []byte) (err error) {
	end := []byte{frameEnd}
	size := uint(len(payload))

	_, err = w.Write([]byte{
		byte(typ),
		byte((channel & 0xff00) >> 8),
		byte((channel & 0x00ff) >> 0),
		byte((size & 0xff000000) >> 24),
		byte((size & 0x00ff0000) >> 16),
		byte((size & 0x0000ff00) >> 8),
		byte((size & 0x000000ff) >> 0),
	})

	if err != nil {
		return
	}

	if _, err = w.Write(payload); err != nil {
		return
	}

	if _, err = w.Write(end); err != nil {
		return
	}

	return
}
