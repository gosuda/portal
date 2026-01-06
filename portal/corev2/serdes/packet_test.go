package serdes

import (
	"encoding/binary"
	"testing"

	"gosuda.org/portal/portal/corev2/common"
)

func TestHeaderBasics(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("testsession12345"))

	header := NewHeader(common.TypeDataKCP, sessionID, 123, 456)

	if header.Magic[0] != 'P' || header.Magic[1] != '2' {
		t.Fatalf("invalid magic: got %c%c", header.Magic[0], header.Magic[1])
	}

	if header.Version != common.ProtocolVersion {
		t.Fatalf("invalid version: got %d, want %d", header.Version, common.ProtocolVersion)
	}

	if header.Type != common.TypeDataKCP {
		t.Fatalf("invalid type: got %d", header.Type)
	}

	if header.PathID != 123 {
		t.Fatalf("invalid path ID: got %d", header.PathID)
	}

	if header.PktSeq != 456 {
		t.Fatalf("invalid packet seq: got %d", header.PktSeq)
	}
}

func TestHeaderSessionID(t *testing.T) {
	var sessionID common.SessionID
	for i := range sessionID {
		sessionID[i] = byte(i)
	}

	header := NewHeader(common.TypeDataKCP, sessionID, 0, 0)
	recovered := header.SessionID()

	if recovered != sessionID {
		t.Fatalf("session ID mismatch: got %v, want %v", recovered, sessionID)
	}
}

func TestHeaderFlags(t *testing.T) {
	header := NewHeader(common.TypeDataKCP, [16]byte{}, 0, 0)

	if header.IsEncrypted() {
		t.Fatal("encrypted flag should be false initially")
	}

	header.SetEncrypted()
	if !header.IsEncrypted() {
		t.Fatal("encrypted flag should be true after SetEncrypted")
	}

	header.ClearEncrypted()
	if header.IsEncrypted() {
		t.Fatal("encrypted flag should be false after ClearEncrypted")
	}

	header.SetKeyPhase(true)
	if header.Flags&common.FlagKeyPhase == 0 {
		t.Fatal("key phase flag not set")
	}

	header.SetKeyPhase(false)
	if header.Flags&common.FlagKeyPhase != 0 {
		t.Fatal("key phase flag should be cleared")
	}

	header.SetAckEliciting()
	if header.Flags&common.FlagAckEli == 0 {
		t.Fatal("ack eliciting flag not set")
	}
}

func TestHeaderSentTime(t *testing.T) {
	header := NewHeader(common.TypeDataKCP, [16]byte{}, 0, 0)

	if header.HasSentTime() {
		t.Fatal("sent time should not be set initially")
	}

	var timeNs common.TimestampNs = 1234567890
	header.SetSentTime(timeNs)

	if !header.HasSentTime() {
		t.Fatal("sent time should be set after SetSentTime")
	}

	if header.SentTimeNs != timeNs {
		t.Fatalf("sent time mismatch: got %d, want %d", header.SentTimeNs, timeNs)
	}
}

func TestHeaderExtensions(t *testing.T) {
	header := NewHeader(common.TypeDataKCP, [16]byte{}, 0, 0)

	if header.HasExtensions() {
		t.Fatal("should not have extensions initially")
	}

	ext := Extension{
		Type:   common.ExtPathClass,
		Length: 1,
		Value:  []byte{common.PathClassLowLatency},
	}

	header.AddExtension(ext)

	if !header.HasExtensions() {
		t.Fatal("should have extensions after AddExtension")
	}

	if len(header.Extensions) != 1 {
		t.Fatalf("should have 1 extension, got %d", len(header.Extensions))
	}

	if header.Extensions[0].Type != common.ExtPathClass {
		t.Fatalf("extension type mismatch: got %d", header.Extensions[0].Type)
	}

	expectedHeaderLen := MinHeaderLen + ext.TotalSize()
	expectedHeaderLen = ((expectedHeaderLen + 3) / 4) * 4

	if header.HeaderLen != uint16(expectedHeaderLen) {
		t.Fatalf("header len mismatch: got %d, want %d", header.HeaderLen, expectedHeaderLen)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("roundtrip-session"))

	header := NewHeader(common.TypeDataKCP, sessionID, 999, 888)
	header.SetEncrypted()
	var timeNs common.TimestampNs = 1111111111111
	header.SetSentTime(timeNs)
	header.SetAckEliciting()

	ext1 := Extension{
		Type:   common.ExtPathClass,
		Length: 1,
		Value:  []byte{common.PathClassBulk},
	}
	ext2 := Extension{
		Type:   common.ExtMetadata,
		Length: 4,
		Value:  []byte("meta"),
	}
	header.AddExtension(ext1)
	header.AddExtension(ext2)

	buf := make([]byte, header.SerializeSize())
	if err := header.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	recovered, err := DeserializeHeader(buf)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if recovered.Type != header.Type {
		t.Fatalf("type mismatch: got %d, want %d", recovered.Type, header.Type)
	}

	if recovered.Flags != header.Flags {
		t.Fatalf("flags mismatch: got %d, want %d", recovered.Flags, header.Flags)
	}

	if recovered.HeaderLen != header.HeaderLen {
		t.Fatalf("header len mismatch: got %d, want %d", recovered.HeaderLen, header.HeaderLen)
	}

	if recovered.SessionID() != sessionID {
		t.Fatalf("session ID mismatch")
	}

	if recovered.PathID != header.PathID {
		t.Fatalf("path ID mismatch")
	}

	if recovered.PktSeq != header.PktSeq {
		t.Fatalf("packet seq mismatch")
	}

	if recovered.SentTimeNs != header.SentTimeNs {
		t.Fatalf("sent time mismatch")
	}

	if len(recovered.Extensions) != len(header.Extensions) {
		t.Fatalf("extension count mismatch: got %d, want %d", len(recovered.Extensions), len(header.Extensions))
	}

	for i := range header.Extensions {
		if recovered.Extensions[i].Type != header.Extensions[i].Type {
			t.Fatalf("extension %d type mismatch", i)
		}
		if recovered.Extensions[i].Length != header.Extensions[i].Length {
			t.Fatalf("extension %d length mismatch", i)
		}
		if len(recovered.Extensions[i].Value) != len(header.Extensions[i].Value) {
			t.Fatalf("extension %d value length mismatch", i)
		}
		for j := range header.Extensions[i].Value {
			if recovered.Extensions[i].Value[j] != header.Extensions[i].Value[j] {
				t.Fatalf("extension %d value mismatch at position %d", i, j)
			}
		}
	}
}

func TestHeaderInvalidMagic(t *testing.T) {
	data := make([]byte, MinHeaderLen)
	data[0] = 'X'
	data[1] = 'Y'

	_, err := DeserializeHeader(data)
	if err == nil {
		t.Fatal("expected error for invalid magic")
	}

	if err != common.ErrInvalidMagic {
		t.Fatalf("expected ErrInvalidMagic, got %v", err)
	}
}

func TestHeaderInvalidVersion(t *testing.T) {
	data := make([]byte, MinHeaderLen)
	copy(data[0:2], "P2")
	data[2] = 0x99

	_, err := DeserializeHeader(data)
	if err == nil {
		t.Fatal("expected error for invalid version")
	}

	if err != common.ErrInvalidVersion {
		t.Fatalf("expected ErrInvalidVersion, got %v", err)
	}
}

func TestHeaderInvalidLength(t *testing.T) {
	data := make([]byte, 10)

	_, err := DeserializeHeader(data)
	if err == nil {
		t.Fatal("expected error for invalid length")
	}

	if err != common.ErrInvalidLength {
		t.Fatalf("expected ErrInvalidLength, got %v", err)
	}
}

func TestHeaderInvalidHeaderLen(t *testing.T) {
	data := make([]byte, MinHeaderLen)
	copy(data[0:2], "P2")
	data[2] = common.ProtocolVersion

	var headerLen uint16 = 47
	data[6] = byte(headerLen >> 8)
	data[7] = byte(headerLen)

	_, err := DeserializeHeader(data)
	if err == nil {
		t.Fatal("expected error for invalid header len (not multiple of 4)")
	}

	if err != common.ErrInvalidHeaderLen {
		t.Fatalf("expected ErrInvalidHeaderLen, got %v", err)
	}
}

func TestHeaderReservedNotZero(t *testing.T) {
	data := make([]byte, MinHeaderLen)
	copy(data[0:2], "P2")
	data[2] = common.ProtocolVersion

	var reserved uint16 = 0xFF
	data[8] = byte(reserved >> 8)
	data[9] = byte(reserved)

	_, err := DeserializeHeader(data)
	if err == nil {
		t.Fatal("expected error for non-zero reserved field")
	}

	if err != common.ErrReservedNotZero {
		t.Fatalf("expected ErrReservedNotZero, got %v", err)
	}
}

func TestPacketBasic(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("test-packet"))

	header := NewHeader(common.TypeDataKCP, sessionID, 1, 1)
	payload := []byte("test payload data")

	packet := NewPacket(header, payload)

	if len(packet.Payload) != len(payload) {
		t.Fatalf("payload length mismatch: got %d, want %d", len(packet.Payload), len(payload))
	}

	for i := range payload {
		if packet.Payload[i] != payload[i] {
			t.Fatalf("payload content mismatch at position %d: got %d, want %d", i, packet.Payload[i], payload[i])
		}
	}
}

func TestPacketRoundTrip(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("test-roundtrip-packet"))

	header := NewHeader(common.TypeDataKCP, sessionID, 100, 200)
	header.SetEncrypted()
	var timeNs common.TimestampNs = 9876543210
	header.SetSentTime(timeNs)

	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	packet := NewPacket(header, payload)

	buf := make([]byte, packet.SerializeSize())
	if err := packet.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	recovered, err := DeserializePacket(buf)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if recovered.Header.Type != header.Type {
		t.Fatalf("type mismatch")
	}

	if recovered.Header.Flags != header.Flags {
		t.Fatalf("flags mismatch")
	}

	if recovered.Header.SessionID() != sessionID {
		t.Fatalf("session ID mismatch")
	}

	if recovered.Header.PathID != header.PathID {
		t.Fatalf("path ID mismatch")
	}

	if recovered.Header.PktSeq != header.PktSeq {
		t.Fatalf("packet seq mismatch")
	}

	if recovered.Header.SentTimeNs != header.SentTimeNs {
		t.Fatalf("sent time mismatch")
	}

	if len(recovered.Payload) != len(payload) {
		t.Fatalf("payload length mismatch")
	}

	for i := range payload {
		if recovered.Payload[i] != payload[i] {
			t.Fatalf("payload mismatch at position %d", i)
		}
	}
}

func TestPacketWithExtensions(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("ext-packet"))

	header := NewHeader(common.TypeProbeReq, sessionID, 0, 0)
	ext := Extension{
		Type:   common.ExtECN,
		Length: 1,
		Value:  []byte{0x03},
	}
	header.AddExtension(ext)

	payload := []byte("probe data")
	packet := NewPacket(header, payload)

	buf := make([]byte, packet.SerializeSize())
	if err := packet.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	recovered, err := DeserializePacket(buf)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if !recovered.Header.HasExtensions() {
		t.Fatal("extensions flag should be set")
	}

	if len(recovered.Header.Extensions) != 1 {
		t.Fatalf("should have 1 extension, got %d", len(recovered.Header.Extensions))
	}

	if recovered.Header.Extensions[0].Type != common.ExtECN {
		t.Fatal("extension type mismatch")
	}
}

func TestPacketEmptyPayload(t *testing.T) {
	header := NewHeader(common.TypeDataKCP, [16]byte{}, 0, 0)
	packet := NewPacket(header, []byte{})

	if packet.Header.PayloadLen != 0 {
		t.Fatalf("payload len should be 0, got %d", packet.Header.PayloadLen)
	}

	buf := make([]byte, packet.SerializeSize())
	if err := packet.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	recovered, err := DeserializePacket(buf)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if len(recovered.Payload) != 0 {
		t.Fatalf("payload should be empty, got %d bytes", len(recovered.Payload))
	}
}

func TestPacketLargePayload(t *testing.T) {
	header := NewHeader(common.TypeDataKCP, [16]byte{}, 0, 0)
	largePayload := make([]byte, 65535)

	for i := range largePayload {
		largePayload[i] = byte(i % 256)
	}

	packet := NewPacket(header, largePayload)

	buf := make([]byte, packet.SerializeSize())
	if err := packet.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	recovered, err := DeserializePacket(buf)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if len(recovered.Payload) != len(largePayload) {
		t.Fatalf("payload length mismatch")
	}

	for i := range largePayload {
		if recovered.Payload[i] != largePayload[i] {
			t.Fatalf("payload mismatch at position %d", i)
		}
	}
}

func TestPacketInvalidLengthPrefix(t *testing.T) {
	data := []byte{0xFF, 0xFF, 0xFF, 0xFF}

	_, err := DeserializePacket(data)
	if err == nil {
		t.Fatal("expected error for invalid length prefix")
	}
}

func TestPacketTruncatedData(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("truncated"))

	header := NewHeader(common.TypeDataKCP, sessionID, 0, 0)
	payload := []byte("test")
	packet := NewPacket(header, payload)

	buf := make([]byte, packet.SerializeSize())
	var err error
	if err = packet.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	truncatedData := buf[:len(buf)-5]

	_, err = DeserializePacket(truncatedData)
	if err == nil {
		t.Fatal("expected error for truncated data")
	}
}

func TestPacketTooLarge(t *testing.T) {
	header := NewHeader(common.TypeDataKCP, [16]byte{}, 0, 0)
	payload := make([]byte, 256)
	packet := NewPacket(header, payload)

	buf := make([]byte, packet.SerializeSize())
	if err := packet.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	binary.BigEndian.PutUint32(buf[0:4], common.MaxPacketSize+1)

	_, err := DeserializePacket(buf)
	if err != common.ErrPacketTooLarge {
		t.Fatalf("expected ErrPacketTooLarge, got %v", err)
	}
}

func TestPacketTooSmall(t *testing.T) {
	header := NewHeader(common.TypeDataKCP, [16]byte{}, 0, 0)
	payload := make([]byte, 256)
	packet := NewPacket(header, payload)

	buf := make([]byte, packet.SerializeSize())
	if err := packet.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	binary.BigEndian.PutUint32(buf[0:4], common.MinPacketSize-1)

	_, err := DeserializePacket(buf)
	if err != common.ErrPacketTooSmall {
		t.Fatalf("expected ErrPacketTooSmall, got %v", err)
	}
}

func TestPacketJumboFrame(t *testing.T) {
	header := NewHeader(common.TypeDataKCP, [16]byte{}, 0, 0)
	payload := make([]byte, 256)
	packet := NewPacket(header, payload)

	buf := make([]byte, packet.SerializeSize())
	if err := packet.Serialize(buf); err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	binary.BigEndian.PutUint32(buf[0:4], common.JumboFrameMarker)

	_, err := DeserializePacket(buf)
	if err != common.ErrJumboFrameRejected {
		t.Fatalf("expected ErrJumboFrameRejected, got %v", err)
	}
}
