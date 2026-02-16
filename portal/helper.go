package portal

import (
	"fmt"
	"io"

	"github.com/rs/zerolog/log"
	"github.com/valyala/bytebufferpool"

	"gosuda.org/portal/portal/core/proto/rdverb"
)

func bufferGrow(buffer *bytebufferpool.ByteBuffer, n int) {
	if n > cap(buffer.B) {
		if n > maxRawPacketSize {
			n = maxRawPacketSize
		}
		newSize := ((n + (1 << 14) - 1) / (1 << 14)) * (1 << 14)
		buffer.B = make([]byte, newSize)
	}
}

func writePacket(w io.Writer, packet *rdverb.Packet) error {
	payload, err := packet.MarshalVT()
	if err != nil {
		return err
	}
	if len(payload) > maxRawPacketSize {
		return fmt.Errorf("packet payload too large: %d", len(payload))
	}

	buffer := bytebufferpool.Get()
	defer bytebufferpool.Put(buffer)

	size := []byte{
		byte(len(payload) >> 24),
		byte(len(payload) >> 16),
		byte(len(payload) >> 8),
		byte(len(payload)),
	}
	if _, err = buffer.Write(size); err != nil {
		return err
	}
	if _, err = buffer.Write(payload); err != nil {
		return err
	}
	_, err = w.Write(buffer.B)
	return err
}

func closeWithLog(closer io.Closer, message string) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		log.Error().Err(err).Msg(message)
	}
}
