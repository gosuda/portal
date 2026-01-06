package serdes

import (
	"testing"

	"gosuda.org/portal/portal/corev2/common"
)

func TestProbeRequest(t *testing.T) {
	var sendTimeNs common.TimestampNs = 1234567890000000
	probeReq := NewProbeRequest(999, sendTimeNs)

	if probeReq.ProbeID != 999 {
		t.Fatalf("probe ID mismatch: got %d, want 999", probeReq.ProbeID)
	}

	if probeReq.SendTimeNs != sendTimeNs {
		t.Fatalf("send time mismatch: got %d, want %d", probeReq.SendTimeNs, sendTimeNs)
	}
}

func TestProbeRequestSerialize(t *testing.T) {
	var sendTimeNs common.TimestampNs = 9876543210000000
	probeReq := NewProbeRequest(12345, sendTimeNs)

	data, err := probeReq.Serialize()
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	if len(data) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(data))
	}

	recovered, err := DeserializeProbeRequest(data)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if recovered.ProbeID != probeReq.ProbeID {
		t.Fatalf("probe ID mismatch after round-trip")
	}

	if recovered.SendTimeNs != probeReq.SendTimeNs {
		t.Fatalf("send time mismatch after round-trip")
	}
}

func TestProbeResponse(t *testing.T) {
	var recvTimeNs common.TimestampNs = 1111111110000000
	var sendTimeNs common.TimestampNs = 1111112222000000
	var processTimeNs common.TimestampNs = 1111113333000000

	probeResp := NewProbeResponse(777, recvTimeNs, sendTimeNs, processTimeNs)

	if probeResp.ProbeID != 777 {
		t.Fatalf("probe ID mismatch: got %d, want 777", probeResp.ProbeID)
	}

	if probeResp.RecvTimeNs != recvTimeNs {
		t.Fatalf("recv time mismatch")
	}

	if probeResp.SendTimeNs != sendTimeNs {
		t.Fatalf("send time mismatch")
	}

	if probeResp.ProcessTimeNs != processTimeNs {
		t.Fatalf("process time mismatch")
	}
}

func TestProbeResponseSerialize(t *testing.T) {
	var recvTimeNs common.TimestampNs = 2222222222000000
	var sendTimeNs common.TimestampNs = 2222223333000000
	var processTimeNs common.TimestampNs = 2222224444000000

	probeResp := NewProbeResponse(54321, recvTimeNs, sendTimeNs, processTimeNs)

	data, err := probeResp.Serialize()
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	if len(data) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(data))
	}

	recovered, err := DeserializeProbeResponse(data)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if recovered.ProbeID != probeResp.ProbeID {
		t.Fatalf("probe ID mismatch after round-trip")
	}

	if recovered.RecvTimeNs != probeResp.RecvTimeNs {
		t.Fatalf("recv time mismatch after round-trip")
	}

	if recovered.SendTimeNs != probeResp.SendTimeNs {
		t.Fatalf("send time mismatch after round-trip")
	}

	if recovered.ProcessTimeNs != probeResp.ProcessTimeNs {
		t.Fatalf("process time mismatch after round-trip")
	}
}

func TestProbeRequestInvalidLength(t *testing.T) {
	shortData := make([]byte, 10)

	_, err := DeserializeProbeRequest(shortData)
	if err == nil {
		t.Fatal("expected error for short data")
	}

	if err != common.ErrInvalidLength {
		t.Fatalf("expected ErrInvalidLength, got %v", err)
	}
}

func TestProbeResponseInvalidLength(t *testing.T) {
	shortData := make([]byte, 20)

	_, err := DeserializeProbeResponse(shortData)
	if err == nil {
		t.Fatal("expected error for short data")
	}

	if err != common.ErrInvalidLength {
		t.Fatalf("expected ErrInvalidLength, got %v", err)
	}
}

func TestProbePacketCreation(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("probe-session"))

	header := NewHeader(common.TypeProbeReq, sessionID, 1, 1)
	var sendTimeNs common.TimestampNs = 3333333333333333
	probeReq := NewProbeRequest(8888, sendTimeNs)

	packet, err := CreateProbePacket(header, probeReq)
	if err != nil {
		t.Fatalf("create probe packet failed: %v", err)
	}

	if packet.Header.Type != common.TypeProbeReq {
		t.Fatalf("packet type mismatch: got %d, want %d", packet.Header.Type, common.TypeProbeReq)
	}

	data, err := probeReq.Serialize()
	if err != nil {
		t.Fatalf("serialize probe request failed: %v", err)
	}

	for i := range data {
		if packet.Payload[i] != data[i] {
			t.Fatalf("payload mismatch at position %d", i)
		}
	}
}

func TestProbeRespPacketCreation(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("probe-response"))

	header := NewHeader(common.TypeProbeResp, sessionID, 2, 2)
	var recvTimeNs common.TimestampNs = 4444444444000000
	var sendTimeNs common.TimestampNs = 4444444445000000
	var processTimeNs common.TimestampNs = 4444445555000000

	probeResp := NewProbeResponse(6666, recvTimeNs, sendTimeNs, processTimeNs)

	packet, err := CreateProbeRespPacket(header, probeResp)
	if err != nil {
		t.Fatalf("create probe response packet failed: %v", err)
	}

	if packet.Header.Type != common.TypeProbeResp {
		t.Fatalf("packet type mismatch")
	}

	data, err := probeResp.Serialize()
	if err != nil {
		t.Fatalf("serialize probe response failed: %v", err)
	}

	for i := range data {
		if packet.Payload[i] != data[i] {
			t.Fatalf("payload mismatch at position %d", i)
		}
	}
}

func TestProbePacketRoundTrip(t *testing.T) {
	var sessionID common.SessionID
	copy(sessionID[:], []byte("probe-roundtrip"))

	header := NewHeader(common.TypeProbeReq, sessionID, 10, 20)
	var sendTimeNs common.TimestampNs = 5555555555555555
	probeReq := NewProbeRequest(12345678, sendTimeNs)

	originalPacket, err := CreateProbePacket(header, probeReq)
	if err != nil {
		t.Fatalf("create packet failed: %v", err)
	}

	buf := make([]byte, originalPacket.SerializeSize())
	if err = originalPacket.Serialize(buf); err != nil {
		t.Fatalf("serialize packet failed: %v", err)
	}

	recoveredPacket, err := DeserializePacket(buf)
	if err != nil {
		t.Fatalf("deserialize packet failed: %v", err)
	}

	recoveredProbeReq, err := DeserializeProbeRequest(recoveredPacket.Payload)
	if err != nil {
		t.Fatalf("deserialize probe request failed: %v", err)
	}

	if recoveredProbeReq.ProbeID != probeReq.ProbeID {
		t.Fatalf("probe ID mismatch after full round-trip")
	}

	if recoveredProbeReq.SendTimeNs != probeReq.SendTimeNs {
		t.Fatalf("send time mismatch after full round-trip")
	}
}
