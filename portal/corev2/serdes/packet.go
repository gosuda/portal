package serdes

import (
	"encoding/binary"

	"gosuda.org/portal/portal/corev2/common"
)

const (
	MinHeaderLen = 48
)

type Header struct {
	Magic       [2]byte
	Version     byte
	Type        byte
	Flags       uint16
	HeaderLen   uint16
	Reserved    uint16
	SessionIDHi uint64
	SessionIDLo uint64
	PathID      common.PathID
	PktSeq      common.PacketSeq
	SentTimeNs  common.TimestampNs
	PayloadLen  uint16
	Extensions  []Extension
}

type Extension struct {
	Type   byte
	Length byte
	Value  []byte
}

func NewHeader(packetType byte, sessionID common.SessionID, pathID common.PathID, seq common.PacketSeq) *Header {
	h := &Header{
		Magic:      [2]byte{'P', '2'},
		Version:    common.ProtocolVersion,
		Type:       packetType,
		Flags:      0,
		HeaderLen:  MinHeaderLen,
		Reserved:   0,
		PathID:     pathID,
		PktSeq:     seq,
		SentTimeNs: 0,
		PayloadLen: 0,
		Extensions: nil,
	}

	h.SetSessionID(sessionID)
	return h
}

func (h *Header) SetSessionID(id common.SessionID) {
	h.SessionIDHi = binary.BigEndian.Uint64(id[0:8])
	h.SessionIDLo = binary.BigEndian.Uint64(id[8:16])
}

func (h *Header) SessionID() common.SessionID {
	var id common.SessionID
	binary.BigEndian.PutUint64(id[0:8], h.SessionIDHi)
	binary.BigEndian.PutUint64(id[8:16], h.SessionIDLo)
	return id
}

func (h *Header) SetEncrypted() {
	h.Flags |= common.FlagEncrypted
}

func (h *Header) ClearEncrypted() {
	h.Flags &^= common.FlagEncrypted
}

func (h *Header) IsEncrypted() bool {
	return h.Flags&common.FlagEncrypted != 0
}

func (h *Header) SetKeyPhase(phase bool) {
	if phase {
		h.Flags |= common.FlagKeyPhase
	} else {
		h.Flags &^= common.FlagKeyPhase
	}
}

func (h *Header) SetAckEliciting() {
	h.Flags |= common.FlagAckEli
}

func (h *Header) HasExtensions() bool {
	return h.Flags&common.FlagHasExt != 0
}

func (h *Header) SetSentTime(ns common.TimestampNs) {
	h.SentTimeNs = ns
	h.Flags |= common.FlagHasTime
}

func (h *Header) HasSentTime() bool {
	return h.Flags&common.FlagHasTime != 0
}

func (h *Header) AddExtension(ext Extension) {
	h.Extensions = append(h.Extensions, ext)
	h.Flags |= common.FlagHasExt
	h.recalcHeaderLen()
}

func (ext *Extension) TotalSize() int {
	return 2 + int(ext.Length)
}

func (h *Header) recalcHeaderLen() {
	extSize := 0
	for _, ext := range h.Extensions {
		extSize += ext.TotalSize()
	}

	totalLen := MinHeaderLen + extSize

	rem := totalLen % 4
	if rem != 0 {
		totalLen = totalLen - rem + 4
	}

	h.HeaderLen = uint16(totalLen)
}

func (h *Header) SerializeSize() int {
	return int(h.HeaderLen)
}

func (h *Header) Serialize(dst []byte) error {
	if len(dst) < int(h.HeaderLen) {
		return common.ErrInvalidLength
	}

	pos := 0

	dst[pos] = h.Magic[0]
	dst[pos+1] = h.Magic[1]
	pos += 2

	dst[pos] = h.Version
	pos++

	dst[pos] = h.Type
	pos++

	binary.BigEndian.PutUint16(dst[pos:pos+2], h.Flags)
	pos += 2

	binary.BigEndian.PutUint16(dst[pos:pos+2], h.HeaderLen)
	pos += 2

	binary.BigEndian.PutUint16(dst[pos:pos+2], h.Reserved)
	pos += 2

	binary.BigEndian.PutUint64(dst[pos:pos+8], h.SessionIDHi)
	pos += 8

	binary.BigEndian.PutUint64(dst[pos:pos+8], h.SessionIDLo)
	pos += 8

	binary.BigEndian.PutUint32(dst[pos:pos+4], uint32(h.PathID))
	pos += 4

	binary.BigEndian.PutUint32(dst[pos:pos+4], uint32(h.PktSeq))
	pos += 4

	binary.BigEndian.PutUint64(dst[pos:pos+8], uint64(h.SentTimeNs))
	pos += 8

	binary.BigEndian.PutUint16(dst[pos:pos+2], h.PayloadLen)
	pos += 2

	for i := 0; i < 4; i++ {
		dst[pos+i] = 0
	}
	pos += 4

	for _, ext := range h.Extensions {
		dst[pos] = ext.Type
		pos++
		dst[pos] = ext.Length
		pos++
		copy(dst[pos:], ext.Value)
		pos += int(ext.Length)
	}

	for pos < int(h.HeaderLen) {
		dst[pos] = 0
		pos++
	}

	return nil
}

func DeserializeHeader(data []byte) (*Header, error) {
	if len(data) < MinHeaderLen {
		return nil, common.ErrInvalidLength
	}

	h := &Header{}
	pos := 0

	h.Magic[0] = data[pos]
	h.Magic[1] = data[pos+1]
	pos += 2

	if h.Magic[0] != 'P' || h.Magic[1] != '2' {
		return nil, common.ErrInvalidMagic
	}

	h.Version = data[pos]
	pos++

	if h.Version != common.ProtocolVersion {
		return nil, common.ErrInvalidVersion
	}

	h.Type = data[pos]
	pos++

	h.Flags = binary.BigEndian.Uint16(data[pos : pos+2])
	pos += 2

	h.HeaderLen = binary.BigEndian.Uint16(data[pos : pos+2])
	pos += 2

	if h.HeaderLen%4 != 0 {
		return nil, common.ErrInvalidHeaderLen
	}

	if int(h.HeaderLen) > len(data) {
		return nil, common.ErrInvalidLength
	}

	h.Reserved = binary.BigEndian.Uint16(data[pos : pos+2])
	pos += 2

	if h.Reserved != 0 {
		return nil, common.ErrReservedNotZero
	}

	h.SessionIDHi = binary.BigEndian.Uint64(data[pos : pos+8])
	pos += 8

	h.SessionIDLo = binary.BigEndian.Uint64(data[pos : pos+8])
	pos += 8

	h.PathID = common.PathID(binary.BigEndian.Uint32(data[pos : pos+4]))
	pos += 4

	h.PktSeq = common.PacketSeq(binary.BigEndian.Uint32(data[pos : pos+4]))
	pos += 4

	h.SentTimeNs = common.TimestampNs(binary.BigEndian.Uint64(data[pos : pos+8]))
	pos += 8

	h.PayloadLen = binary.BigEndian.Uint16(data[pos : pos+2])
	pos += 2

	pos += 4

	if h.HasExtensions() {
		h.Extensions = []Extension{}
		headerEnd := int(h.HeaderLen)
		pos = 48

		for pos < headerEnd && pos+2 <= len(data) {
			extType := data[pos]
			extLen := data[pos+1]

			if extType == 0 && extLen == 0 {
				break
			}

			pos += 2

			if pos+int(extLen) > headerEnd {
				return nil, common.ErrInvalidLength
			}

			extValue := make([]byte, extLen)
			copy(extValue, data[pos:pos+int(extLen)])
			pos += int(extLen)

			h.Extensions = append(h.Extensions, Extension{
				Type:   extType,
				Length: extLen,
				Value:  extValue,
			})
		}
	}

	return h, nil
}

type Packet struct {
	Header  *Header
	Payload []byte
}

func NewPacket(header *Header, payload []byte) *Packet {
	pkt := &Packet{
		Header:  header,
		Payload: payload,
	}
	pkt.Header.PayloadLen = uint16(len(payload))
	return pkt
}

func (p *Packet) SerializeSize() int {
	return 4 + p.Header.SerializeSize() + len(p.Payload)
}

func (p *Packet) Serialize(dst []byte) error {
	requiredSize := p.SerializeSize()
	if len(dst) < requiredSize {
		return common.ErrInvalidLength
	}

	pos := 0

	totalLen := p.Header.SerializeSize() + len(p.Payload)
	binary.BigEndian.PutUint32(dst[pos:pos+4], uint32(totalLen))
	pos += 4

	headerSize := p.Header.SerializeSize()
	if err := p.Header.Serialize(dst[pos : pos+headerSize]); err != nil {
		return err
	}
	pos += headerSize

	copy(dst[pos:], p.Payload)

	return nil
}

func DeserializePacket(data []byte) (*Packet, error) {
	if len(data) < 4 {
		return nil, common.ErrInvalidLength
	}

	pos := 0

	totalLen := int(binary.BigEndian.Uint32(data[pos : pos+4]))
	pos += 4

	if totalLen == int(common.JumboFrameMarker) {
		return nil, common.ErrJumboFrameRejected
	}

	if totalLen < int(common.MinPacketSize) {
		return nil, common.ErrPacketTooSmall
	}

	if totalLen > int(common.MaxPacketSize) {
		return nil, common.ErrPacketTooLarge
	}

	if len(data) < 4+totalLen {
		return nil, common.ErrInvalidLength
	}

	headerLen := int(binary.BigEndian.Uint16(data[6:8]))

	header, err := DeserializeHeader(data[pos : pos+headerLen])
	if err != nil {
		return nil, err
	}

	payloadStart := 4 + int(header.HeaderLen)
	payloadEnd := payloadStart + int(header.PayloadLen)
	if payloadEnd > len(data) {
		return nil, common.ErrInvalidLength
	}

	payload := data[payloadStart:payloadEnd]

	return &Packet{
		Header:  header,
		Payload: payload,
	}, nil
}
