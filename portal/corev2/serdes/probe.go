package serdes

import (
	"bytes"
	"encoding/binary"

	"gosuda.org/portal/portal/corev2/common"
)

type ProbeRequest struct {
	ProbeID    uint64
	SendTimeNs common.TimestampNs
}

type ProbeResponse struct {
	ProbeID       uint64
	RecvTimeNs    common.TimestampNs
	SendTimeNs    common.TimestampNs
	ProcessTimeNs common.TimestampNs
}

func NewProbeRequest(probeID uint64, sendTimeNs common.TimestampNs) *ProbeRequest {
	return &ProbeRequest{
		ProbeID:    probeID,
		SendTimeNs: sendTimeNs,
	}
}

func NewProbeResponse(probeID uint64, recvTimeNs, sendTimeNs, processTimeNs common.TimestampNs) *ProbeResponse {
	return &ProbeResponse{
		ProbeID:       probeID,
		RecvTimeNs:    recvTimeNs,
		SendTimeNs:    sendTimeNs,
		ProcessTimeNs: processTimeNs,
	}
}

func (pr *ProbeRequest) Serialize() ([]byte, error) {
	buf := new(bytes.Buffer)

	if err := binary.Write(buf, binary.BigEndian, pr.ProbeID); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, pr.SendTimeNs); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func DeserializeProbeRequest(data []byte) (*ProbeRequest, error) {
	if len(data) != 16 {
		return nil, common.ErrInvalidLength
	}

	buf := bytes.NewReader(data)
	pr := &ProbeRequest{}

	if err := binary.Read(buf, binary.BigEndian, &pr.ProbeID); err != nil {
		return nil, err
	}

	if err := binary.Read(buf, binary.BigEndian, &pr.SendTimeNs); err != nil {
		return nil, err
	}

	return pr, nil
}

func (pr *ProbeResponse) Serialize() ([]byte, error) {
	buf := new(bytes.Buffer)

	if err := binary.Write(buf, binary.BigEndian, pr.ProbeID); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, pr.RecvTimeNs); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, pr.SendTimeNs); err != nil {
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, pr.ProcessTimeNs); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func DeserializeProbeResponse(data []byte) (*ProbeResponse, error) {
	if len(data) != 32 {
		return nil, common.ErrInvalidLength
	}

	buf := bytes.NewReader(data)
	pr := &ProbeResponse{}

	if err := binary.Read(buf, binary.BigEndian, &pr.ProbeID); err != nil {
		return nil, err
	}

	if err := binary.Read(buf, binary.BigEndian, &pr.RecvTimeNs); err != nil {
		return nil, err
	}

	if err := binary.Read(buf, binary.BigEndian, &pr.SendTimeNs); err != nil {
		return nil, err
	}

	if err := binary.Read(buf, binary.BigEndian, &pr.ProcessTimeNs); err != nil {
		return nil, err
	}

	return pr, nil
}

func CreateProbePacket(header *Header, req *ProbeRequest) (*Packet, error) {
	payload, err := req.Serialize()
	if err != nil {
		return nil, err
	}
	return NewPacket(header, payload), nil
}

func CreateProbeRespPacket(header *Header, resp *ProbeResponse) (*Packet, error) {
	payload, err := resp.Serialize()
	if err != nil {
		return nil, err
	}
	return NewPacket(header, payload), nil
}
