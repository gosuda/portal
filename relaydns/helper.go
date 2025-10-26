package relaydns

import (
	"encoding/binary"
	"io"

	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/valyala/bytebufferpool"
)

func bufferGrow(buffer *bytebufferpool.ByteBuffer, n int) {
	if n > cap(buffer.B) {
		buffer.B = make([]byte, ((n+(1<<14)-1)/1<<14)*(1<<14))
	}
}

func decodeProtobuf[T interface {
	UnmarshalVT(data []byte) error
}](
	data []byte,
) (
	T,
	error,
) {
	var t T
	err := t.UnmarshalVT(data)
	if err != nil {
		return t, err
	}
	return t, nil
}

func writePacket(w io.Writer, packet *rdverb.Packet) error {
	payload, err := packet.MarshalVT()
	if err != nil {
		return err
	}

	buffer := bytebufferpool.Get()
	defer bytebufferpool.Put(buffer)

	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(payload)))
	buffer.Write(size[:])
	buffer.Write(payload)
	_, err = w.Write(buffer.B)
	return err
}
