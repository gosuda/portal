package portal

import (
	"encoding/binary"
	"io"

	"github.com/valyala/bytebufferpool"

	"gosuda.org/portal/portal/core/proto/rdverb"
)

func bufferGrow(buffer *bytebufferpool.ByteBuffer, n int) {
	if n > cap(buffer.B) {
		if n > _MAX_RAW_PACKET_SIZE {
			n = _MAX_RAW_PACKET_SIZE
		}
		newSize := ((n + (1 << 14) - 1) / (1 << 14)) * (1 << 14)
		buffer.B = make([]byte, newSize)
	}
}

func decodeProtobuf[T interface {
	UnmarshalVT(data []byte) error
}](
	data []byte,
) (
	*T,
	error,
) {
	var t T
	err := t.UnmarshalVT(data)
	if err != nil {
		return nil, err
	}
	return &t, nil
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
