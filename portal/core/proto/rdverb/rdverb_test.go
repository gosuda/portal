package rdverb

import (
	"bytes"
	"testing"

	rdsec "gosuda.org/portal/portal/core/proto/rdsec"
)

// TestPacket_MarshalVT_UnmarshalVT tests round-trip serialization for Packet
func TestPacket_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *Packet
		wantErr bool
	}{
		{
			name:    "empty",
			input:   &Packet{},
			wantErr: false,
		},
		{
			name: "full",
			input: &Packet{
				Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
				Payload: []byte{0x01, 0x02, 0x03, 0x04},
			},
			wantErr: false,
		},
		{
			name: "type only",
			input: &Packet{
				Type: PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST,
			},
			wantErr: false,
		},
		{
			name: "payload only",
			input: &Packet{
				Payload: []byte("test payload"),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.input.MarshalVT()
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &Packet{}
			err = got.UnmarshalVT(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.input.EqualVT(got) {
				t.Errorf("roundtrip mismatch")
			}
		})
	}
}

// TestPacket_AllPacketTypes tests serialization of all packet types
func TestPacket_AllPacketTypes(t *testing.T) {
	packetTypes := []PacketType{
		PacketType_PACKET_TYPE_RELAY_INFO_REQUEST,
		PacketType_PACKET_TYPE_RELAY_INFO_RESPONSE,
		PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST,
		PacketType_PACKET_TYPE_LEASE_UPDATE_RESPONSE,
		PacketType_PACKET_TYPE_LEASE_DELETE_REQUEST,
		PacketType_PACKET_TYPE_LEASE_DELETE_RESPONSE,
		PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
	}

	for _, pt := range packetTypes {
		t.Run(pt.String(), func(t *testing.T) {
			msg := &Packet{
				Type:    pt,
				Payload: []byte("test payload"),
			}

			data, err := msg.MarshalVT()
			if err != nil {
				t.Fatalf("MarshalVT() error = %v", err)
			}

			got := &Packet{}
			if err := got.UnmarshalVT(data); err != nil {
				t.Fatalf("UnmarshalVT() error = %v", err)
			}

			if !msg.EqualVT(got) {
				t.Errorf("roundtrip mismatch for %v", pt)
			}
		})
	}
}

// TestRelayInfo_MarshalVT_UnmarshalVT tests round-trip serialization
func TestRelayInfo_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *RelayInfo
		wantErr bool
	}{
		{
			name:    "empty",
			input:   &RelayInfo{},
			wantErr: false,
		},
		{
			name: "full",
			input: &RelayInfo{
				Identity: &rdsec.Identity{
					Id:        "relay-id",
					PublicKey: []byte{0x01, 0x02},
				},
				Address: []string{"addr1.example.com:8080", "addr2.example.com:8080"},
				Leases: []*Lease{
					{
						Identity: &rdsec.Identity{Id: "lease1-id"},
						Expires:  1234567890,
						Name:     "lease1",
						Alpn:     []string{"h2", "http/1.1"},
						Metadata: "metadata1",
					},
					{
						Identity: &rdsec.Identity{Id: "lease2-id"},
						Expires:  9876543210,
						Name:     "lease2",
						Alpn:     []string{"h2"},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "with identity only",
			input: &RelayInfo{
				Identity: &rdsec.Identity{Id: "test-relay"},
			},
			wantErr: false,
		},
		{
			name: "with multiple addresses",
			input: &RelayInfo{
				Identity: &rdsec.Identity{Id: "multi-addr"},
				Address:  []string{"addr1:8080", "addr2:8080", "addr3:8080"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.input.MarshalVT()
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &RelayInfo{}
			err = got.UnmarshalVT(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.input.EqualVT(got) {
				t.Errorf("roundtrip mismatch")
			}
		})
	}
}

// TestRelayInfo_WithMultipleLeases tests array handling for leases
func TestRelayInfo_WithMultipleLeases(t *testing.T) {
	leases := []*Lease{
		{Identity: &rdsec.Identity{Id: "l1"}, Expires: 100, Name: "lease1"},
		{Identity: &rdsec.Identity{Id: "l2"}, Expires: 200, Name: "lease2"},
		{Identity: &rdsec.Identity{Id: "l3"}, Expires: 300, Name: "lease3"},
		{Identity: &rdsec.Identity{Id: "l4"}, Expires: 400, Name: "lease4"},
		{Identity: &rdsec.Identity{Id: "l5"}, Expires: 500, Name: "lease5"},
	}

	msg := &RelayInfo{
		Identity: &rdsec.Identity{Id: "relay"},
		Leases:   leases,
	}

	data, err := msg.MarshalVT()
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	got := &RelayInfo{}
	if err := got.UnmarshalVT(data); err != nil {
		t.Fatalf("UnmarshalVT() error = %v", err)
	}

	if len(got.Leases) != len(leases) {
		t.Fatalf("got %d leases, want %d", len(got.Leases), len(leases))
	}

	for i, want := range leases {
		if got.Leases[i].Name != want.Name {
			t.Errorf("lease[%d].Name = %v, want %v", i, got.Leases[i].Name, want.Name)
		}
	}
}

// TestLease_MarshalVT_UnmarshalVT tests round-trip serialization
func TestLease_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *Lease
		wantErr bool
	}{
		{
			name:    "empty",
			input:   &Lease{},
			wantErr: false,
		},
		{
			name: "full",
			input: &Lease{
				Identity: &rdsec.Identity{
					Id:        "lease-id",
					PublicKey: []byte{0x01, 0x02},
				},
				Expires:  1234567890,
				Name:     "my-lease",
				Alpn:     []string{"h2", "http/1.1"},
				Metadata: "some metadata",
			},
			wantErr: false,
		},
		{
			name: "with alpn",
			input: &Lease{
				Identity: &rdsec.Identity{Id: "alpn-test"},
				Alpn:     []string{"h2", "grpc"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.input.MarshalVT()
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &Lease{}
			err = got.UnmarshalVT(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.input.EqualVT(got) {
				t.Errorf("roundtrip mismatch")
			}
		})
	}
}

// TestLeaseUpdateRequest_MarshalVT_UnmarshalVT tests round-trip serialization
func TestLeaseUpdateRequest_MarshalVT_UnmarshalVT(t *testing.T) {
	lease := &Lease{
		Identity: &rdsec.Identity{Id: "update-lease"},
		Expires:  1234567890,
		Name:     "lease-name",
	}

	tests := []struct {
		name    string
		input   *LeaseUpdateRequest
		wantErr bool
	}{
		{
			name: "full",
			input: &LeaseUpdateRequest{
				Lease:     lease,
				Nonce:     []byte{0x01, 0x02, 0x03, 0x04},
				Timestamp: 9876543210,
			},
			wantErr: false,
		},
		{
			name:    "empty",
			input:   &LeaseUpdateRequest{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.input.MarshalVT()
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &LeaseUpdateRequest{}
			err = got.UnmarshalVT(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.input.EqualVT(got) {
				t.Errorf("roundtrip mismatch")
			}
		})
	}
}

// TestResponseCode_AllValues tests all response code values
func TestResponseCode_AllValues(t *testing.T) {
	codes := []ResponseCode{
		ResponseCode_RESPONSE_CODE_UNKNOWN,
		ResponseCode_RESPONSE_CODE_ACCEPTED,
		ResponseCode_RESPONSE_CODE_INVALID_EXPIRES,
		ResponseCode_RESPONSE_CODE_INVALID_IDENTITY,
		ResponseCode_RESPONSE_CODE_INVALID_NAME,
		ResponseCode_RESPONSE_CODE_INVALID_ALPN,
		ResponseCode_RESPONSE_CODE_REJECTED,
	}

	for _, code := range codes {
		t.Run(code.String(), func(t *testing.T) {
			msg := &LeaseUpdateResponse{Code: code}

			data, err := msg.MarshalVT()
			if err != nil {
				t.Fatalf("MarshalVT() error = %v", err)
			}

			got := &LeaseUpdateResponse{}
			if err := got.UnmarshalVT(data); err != nil {
				t.Fatalf("UnmarshalVT() error = %v", err)
			}

			if got.Code != code {
				t.Errorf("Code = %v, want %v", got.Code, code)
			}
		})
	}
}

// TestLeaseDeleteRequest_MarshalVT_UnmarshalVT tests round-trip serialization
func TestLeaseDeleteRequest_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *LeaseDeleteRequest
		wantErr bool
	}{
		{
			name: "full",
			input: &LeaseDeleteRequest{
				Identity:  &rdsec.Identity{Id: "delete-id", PublicKey: []byte{0x01}},
				Nonce:     []byte{0x01, 0x02, 0x03},
				Timestamp: 1234567890,
			},
			wantErr: false,
		},
		{
			name:    "empty",
			input:   &LeaseDeleteRequest{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.input.MarshalVT()
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &LeaseDeleteRequest{}
			err = got.UnmarshalVT(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.input.EqualVT(got) {
				t.Errorf("roundtrip mismatch")
			}
		})
	}
}

// TestConnectionRequest_MarshalVT_UnmarshalVT tests round-trip serialization
func TestConnectionRequest_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *ConnectionRequest
		wantErr bool
	}{
		{
			name: "full",
			input: &ConnectionRequest{
				LeaseId:        "lease-123",
				ClientIdentity: &rdsec.Identity{Id: "client-id", PublicKey: []byte{0xAA, 0xBB}},
			},
			wantErr: false,
		},
		{
			name:    "empty",
			input:   &ConnectionRequest{},
			wantErr: false,
		},
		{
			name: "lease id only",
			input: &ConnectionRequest{
				LeaseId: "lease-only",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.input.MarshalVT()
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &ConnectionRequest{}
			err = got.UnmarshalVT(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.input.EqualVT(got) {
				t.Errorf("roundtrip mismatch")
			}
		})
	}
}

// TestRelayInfoRequest_MarshalVT_UnmarshalVT tests empty message
func TestRelayInfoRequest_MarshalVT_UnmarshalVT(t *testing.T) {
	msg := &RelayInfoRequest{}

	data, err := msg.MarshalVT()
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	got := &RelayInfoRequest{}
	err = got.UnmarshalVT(data)
	if err != nil {
		t.Fatalf("UnmarshalVT() error = %v", err)
	}

	if !msg.EqualVT(got) {
		t.Error("roundtrip mismatch for empty RelayInfoRequest")
	}
}

// TestRelayInfoResponse_MarshalVT_UnmarshalVT tests with RelayInfo
func TestRelayInfoResponse_MarshalVT_UnmarshalVT(t *testing.T) {
	relayInfo := &RelayInfo{
		Identity: &rdsec.Identity{Id: "response-relay"},
		Address:  []string{"addr1:8080"},
		Leases: []*Lease{
			{Identity: &rdsec.Identity{Id: "l1"}, Name: "lease1"},
		},
	}

	msg := &RelayInfoResponse{RelayInfo: relayInfo}

	data, err := msg.MarshalVT()
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	got := &RelayInfoResponse{}
	err = got.UnmarshalVT(data)
	if err != nil {
		t.Fatalf("UnmarshalVT() error = %v", err)
	}

	if !msg.EqualVT(got) {
		t.Error("roundtrip mismatch")
	}
}

// TestCloneVT tests cloning creates independent copies
func TestCloneVT(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		original := &Packet{
			Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
			Payload: []byte{0x01, 0x02, 0x03},
		}
		cloned := original.CloneVT()

		cloned.Type = PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST
		cloned.Payload[0] = 0xFF

		if original.Type != PacketType_PACKET_TYPE_CONNECTION_REQUEST {
			t.Error("original.Type was modified")
		}
		if original.Payload[0] != 0x01 {
			t.Error("original.Payload was modified")
		}
	})

	t.Run("RelayInfo", func(t *testing.T) {
		original := &RelayInfo{
			Identity: &rdsec.Identity{Id: "test"},
			Address:  []string{"addr1"},
			Leases: []*Lease{
				{Identity: &rdsec.Identity{Id: "l1"}, Name: "lease1"},
			},
		}
		cloned := original.CloneVT()

		cloned.Identity.Id = "modified"
		cloned.Address[0] = "modified-addr"
		cloned.Leases[0].Name = "modified-lease"

		if original.Identity.Id != "test" {
			t.Error("original.Identity.Id was modified")
		}
		if original.Address[0] != "addr1" {
			t.Error("original.Address was modified")
		}
		if original.Leases[0].Name != "lease1" {
			t.Error("original.Leases[0].Name was modified")
		}
	})

	t.Run("Lease", func(t *testing.T) {
		original := &Lease{
			Identity: &rdsec.Identity{Id: "lease-clone"},
			Expires:  12345,
			Name:     "clone-lease",
			Alpn:     []string{"h2"},
		}
		cloned := original.CloneVT()

		cloned.Identity.Id = "modified"
		cloned.Expires = 99999
		cloned.Name = "modified"
		cloned.Alpn[0] = "modified"

		if original.Identity.Id != "lease-clone" {
			t.Error("original.Identity.Id was modified")
		}
		if original.Expires != 12345 {
			t.Error("original.Expires was modified")
		}
		if original.Name != "clone-lease" {
			t.Error("original.Name was modified")
		}
		if original.Alpn[0] != "h2" {
			t.Error("original.Alpn was modified")
		}
	})
}

// TestEqualVT tests equality comparison
func TestEqualVT(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		a := &Packet{Type: PacketType_PACKET_TYPE_CONNECTION_REQUEST, Payload: []byte{0x01}}
		b := &Packet{Type: PacketType_PACKET_TYPE_CONNECTION_REQUEST, Payload: []byte{0x01}}
		c := &Packet{Type: PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST, Payload: []byte{0x01}}

		if !a.EqualVT(b) {
			t.Error("Equal packets should be equal")
		}
		if a.EqualVT(c) {
			t.Error("Different packet types should not be equal")
		}
		if a.EqualVT(nil) {
			t.Error("Packet should not equal nil")
		}
		if !(*Packet)(nil).EqualVT(nil) {
			t.Error("nil should equal nil")
		}
	})

	t.Run("Lease", func(t *testing.T) {
		identity := &rdsec.Identity{Id: "test"}
		a := &Lease{Identity: identity, Expires: 123, Name: "test"}
		b := &Lease{Identity: identity, Expires: 123, Name: "test"}
		c := &Lease{Identity: identity, Expires: 456, Name: "test"}

		if !a.EqualVT(b) {
			t.Error("Equal leases should be equal")
		}
		if a.EqualVT(c) {
			t.Error("Leases with different Expires should not be equal")
		}
	})
}

// TestSizeVT tests size calculation accuracy
func TestSizeVT(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		msg := &Packet{
			Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
			Payload: []byte("test payload"),
		}

		size := msg.SizeVT()
		data, err := msg.MarshalVT()
		if err != nil {
			t.Fatalf("MarshalVT() error = %v", err)
		}

		if size != len(data) {
			t.Errorf("SizeVT() = %v, MarshalVT() produced %v bytes", size, len(data))
		}
	})

	t.Run("RelayInfo", func(t *testing.T) {
		msg := &RelayInfo{
			Identity: &rdsec.Identity{Id: "size-test"},
			Address:  []string{"addr1:8080", "addr2:8080"},
			Leases: []*Lease{
				{Identity: &rdsec.Identity{Id: "l1"}, Name: "lease1"},
			},
		}

		size := msg.SizeVT()
		data, err := msg.MarshalVT()
		if err != nil {
			t.Fatalf("MarshalVT() error = %v", err)
		}

		if size != len(data) {
			t.Errorf("SizeVT() = %v, MarshalVT() produced %v bytes", size, len(data))
		}
	})

	t.Run("Lease", func(t *testing.T) {
		msg := &Lease{
			Identity: &rdsec.Identity{Id: "size-lease"},
			Expires:  1234567890,
			Name:     "size-test",
			Alpn:     []string{"h2", "http/1.1"},
			Metadata: "size metadata",
		}

		size := msg.SizeVT()
		data, err := msg.MarshalVT()
		if err != nil {
			t.Fatalf("MarshalVT() error = %v", err)
		}

		if size != len(data) {
			t.Errorf("SizeVT() = %v, MarshalVT() produced %v bytes", size, len(data))
		}
	})
}

// TestReset tests Reset clears all fields
func TestReset(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		p := &Packet{Type: PacketType_PACKET_TYPE_CONNECTION_REQUEST, Payload: []byte{0x01}}
		p.Reset()
		if p.Type != 0 || p.Payload != nil {
			t.Error("Packet not properly reset")
		}
	})

	t.Run("Lease", func(t *testing.T) {
		l := &Lease{
			Identity: &rdsec.Identity{Id: "test"},
			Expires:  123,
			Name:     "test-name",
			Alpn:     []string{"h2"},
			Metadata: "meta",
		}
		l.Reset()
		if l.Identity != nil || l.Expires != 0 || l.Name != "" || l.Alpn != nil || l.Metadata != "" {
			t.Error("Lease not properly reset")
		}
	})
}

// TestGetters tests getter methods
func TestGetters(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		p := &Packet{
			Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
			Payload: []byte{0x01, 0x02},
		}

		if p.GetType() != PacketType_PACKET_TYPE_CONNECTION_REQUEST {
			t.Error("GetType() returned wrong value")
		}
		if !bytes.Equal(p.GetPayload(), []byte{0x01, 0x02}) {
			t.Error("GetPayload() returned wrong value")
		}
	})

	t.Run("Lease", func(t *testing.T) {
		identity := &rdsec.Identity{Id: "lease-id", PublicKey: []byte{0x01}}
		l := &Lease{
			Identity: identity,
			Expires:  12345,
			Name:     "lease-name",
			Alpn:     []string{"h2", "grpc"},
			Metadata: "lease-metadata",
		}

		if l.GetIdentity() != identity {
			t.Error("GetIdentity() returned wrong value")
		}
		if l.GetExpires() != 12345 {
			t.Error("GetExpires() returned wrong value")
		}
		if l.GetName() != "lease-name" {
			t.Error("GetName() returned wrong value")
		}
		if len(l.GetAlpn()) != 2 {
			t.Error("GetAlpn() returned wrong value")
		}
		if l.GetMetadata() != "lease-metadata" {
			t.Error("GetMetadata() returned wrong value")
		}
	})

	t.Run("nil Lease", func(t *testing.T) {
		var l *Lease
		if l.GetIdentity() != nil {
			t.Error("GetIdentity() on nil should return nil")
		}
		if l.GetExpires() != 0 {
			t.Error("GetExpires() on nil should return 0")
		}
		if l.GetName() != "" {
			t.Error("GetName() on nil should return empty string")
		}
		if l.GetAlpn() != nil {
			t.Error("GetAlpn() on nil should return nil")
		}
		if l.GetMetadata() != "" {
			t.Error("GetMetadata() on nil should return empty string")
		}
	})
}

// TestNilHandling tests nil message handling
func TestNilHandling(t *testing.T) {
	testCases := []struct {
		name string
		test func(t *testing.T)
	}{
		{"Packet", func(t *testing.T) {
			var msg *Packet
			if data, err := msg.MarshalVT(); err != nil || data != nil {
				t.Errorf("MarshalVT() on nil Packet = (%v, %v), want (nil, nil)", data, err)
			}
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil Packet should return nil")
			}
			if msg.SizeVT() != 0 {
				t.Error("SizeVT() on nil Packet should return 0")
			}
		}},
		{"RelayInfo", func(t *testing.T) {
			var msg *RelayInfo
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil RelayInfo should return nil")
			}
		}},
		{"Lease", func(t *testing.T) {
			var msg *Lease
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil Lease should return nil")
			}
		}},
		{"LeaseUpdateRequest", func(t *testing.T) {
			var msg *LeaseUpdateRequest
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil LeaseUpdateRequest should return nil")
			}
		}},
		{"LeaseUpdateResponse", func(t *testing.T) {
			var msg *LeaseUpdateResponse
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil LeaseUpdateResponse should return nil")
			}
		}},
		{"LeaseDeleteRequest", func(t *testing.T) {
			var msg *LeaseDeleteRequest
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil LeaseDeleteRequest should return nil")
			}
		}},
		{"LeaseDeleteResponse", func(t *testing.T) {
			var msg *LeaseDeleteResponse
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil LeaseDeleteResponse should return nil")
			}
		}},
		{"ConnectionRequest", func(t *testing.T) {
			var msg *ConnectionRequest
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil ConnectionRequest should return nil")
			}
		}},
		{"ConnectionResponse", func(t *testing.T) {
			var msg *ConnectionResponse
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil ConnectionResponse should return nil")
			}
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.test)
	}
}

// TestMarshalVTStrict tests strict marshaling
func TestMarshalVTStrict(t *testing.T) {
	msg := &Packet{
		Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: []byte{0x01, 0x02, 0x03},
	}

	data, err := msg.MarshalVTStrict()
	if err != nil {
		t.Fatalf("MarshalVTStrict() error = %v", err)
	}

	got := &Packet{}
	err = got.UnmarshalVT(data)
	if err != nil {
		t.Fatalf("UnmarshalVT() error = %v", err)
	}

	if !msg.EqualVT(got) {
		t.Error("MarshalVTStrict roundtrip mismatch")
	}
}

// TestUnmarshalVTUnsafe tests unsafe unmarshaling
func TestUnmarshalVTUnsafe(t *testing.T) {
	msg := &ConnectionRequest{
		LeaseId:        "unsafe-test",
		ClientIdentity: &rdsec.Identity{Id: "unsafe-client", PublicKey: []byte{0xAA, 0xBB}},
	}

	data, err := msg.MarshalVT()
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	got := &ConnectionRequest{}
	err = got.UnmarshalVTUnsafe(data)
	if err != nil {
		t.Fatalf("UnmarshalVTUnsafe() error = %v", err)
	}

	if !msg.EqualVT(got) {
		t.Error("UnmarshalVTUnsafe roundtrip mismatch")
	}
}

// TestEmptyResponseMessages tests response messages
func TestEmptyResponseMessages(t *testing.T) {
	responses := []ResponseCode{
		ResponseCode_RESPONSE_CODE_UNKNOWN,
		ResponseCode_RESPONSE_CODE_ACCEPTED,
		ResponseCode_RESPONSE_CODE_INVALID_EXPIRES,
		ResponseCode_RESPONSE_CODE_INVALID_IDENTITY,
		ResponseCode_RESPONSE_CODE_INVALID_NAME,
		ResponseCode_RESPONSE_CODE_INVALID_ALPN,
		ResponseCode_RESPONSE_CODE_REJECTED,
	}

	for _, code := range responses {
		t.Run(code.String(), func(t *testing.T) {
			updateResp := &LeaseUpdateResponse{Code: code}
			data, err := updateResp.MarshalVT()
			if err != nil {
				t.Fatalf("MarshalVT() error = %v", err)
			}

			got := &LeaseUpdateResponse{}
			if err := got.UnmarshalVT(data); err != nil {
				t.Fatalf("UnmarshalVT() error = %v", err)
			}

			if got.Code != code {
				t.Errorf("Code = %v, want %v", got.Code, code)
			}
		})
	}
}

// TestComplexRelayInfo tests complex RelayInfo with multiple nested elements
func TestComplexRelayInfo(t *testing.T) {
	// Create a complex RelayInfo with multiple addresses and leases
	msg := &RelayInfo{
		Identity: &rdsec.Identity{
			Id:        "complex-relay",
			PublicKey: bytes.Repeat([]byte{0xAA}, 32),
		},
		Address: []string{
			"relay1.example.com:443",
			"relay2.example.com:443",
			"relay3.example.com:443",
		},
		Leases: []*Lease{
			{
				Identity: &rdsec.Identity{
					Id:        "lease-1",
					PublicKey: bytes.Repeat([]byte{0x01}, 32),
				},
				Expires:  1000000000,
				Name:     "service-1",
				Alpn:     []string{"h2", "grpc"},
				Metadata: "production service 1",
			},
			{
				Identity: &rdsec.Identity{
					Id:        "lease-2",
					PublicKey: bytes.Repeat([]byte{0x02}, 32),
				},
				Expires:  2000000000,
				Name:     "service-2",
				Alpn:     []string{"h2"},
				Metadata: "production service 2",
			},
		},
	}

	data, err := msg.MarshalVT()
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	got := &RelayInfo{}
	err = got.UnmarshalVT(data)
	if err != nil {
		t.Fatalf("UnmarshalVT() error = %v", err)
	}

	if !msg.EqualVT(got) {
		t.Error("complex RelayInfo roundtrip mismatch")
	}

	// Verify all fields
	if len(got.Address) != 3 {
		t.Errorf("got %d addresses, want 3", len(got.Address))
	}
	if len(got.Leases) != 2 {
		t.Errorf("got %d leases, want 2", len(got.Leases))
	}
}

// BenchmarkPacket_MarshalVT benchmarks packet marshaling
func BenchmarkPacket_MarshalVT(b *testing.B) {
	msg := &Packet{
		Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: bytes.Repeat([]byte{0x01}, 1024),
	}

	b.ResetTimer()
	for range b.N {
		_, _ = msg.MarshalVT()
	}
}

// BenchmarkPacket_UnmarshalVT benchmarks packet unmarshaling
func BenchmarkPacket_UnmarshalVT(b *testing.B) {
	msg := &Packet{
		Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: bytes.Repeat([]byte{0x01}, 1024),
	}

	data, _ := msg.MarshalVT()

	b.ResetTimer()
	for range b.N {
		got := &Packet{}
		_ = got.UnmarshalVT(data)
	}
}

// BenchmarkRelayInfo_MarshalVT benchmarks complex relay info marshaling
func BenchmarkRelayInfo_MarshalVT(b *testing.B) {
	msg := &RelayInfo{
		Identity: &rdsec.Identity{
			Id:        "benchmark-relay",
			PublicKey: bytes.Repeat([]byte{0xAA}, 32),
		},
		Address: []string{"addr1:8080", "addr2:8080", "addr3:8080"},
		Leases: []*Lease{
			{
				Identity: &rdsec.Identity{Id: "l1", PublicKey: bytes.Repeat([]byte{0x01}, 32)},
				Expires:  1234567890,
				Name:     "lease1",
				Alpn:     []string{"h2", "grpc"},
			},
			{
				Identity: &rdsec.Identity{Id: "l2", PublicKey: bytes.Repeat([]byte{0x02}, 32)},
				Expires:  9876543210,
				Name:     "lease2",
				Alpn:     []string{"h2"},
			},
		},
	}

	b.ResetTimer()
	for range b.N {
		_, _ = msg.MarshalVT()
	}
}

// BenchmarkLease_MarshalVT benchmarks lease marshaling
func BenchmarkLease_MarshalVT(b *testing.B) {
	msg := &Lease{
		Identity: &rdsec.Identity{
			Id:        "benchmark-lease",
			PublicKey: bytes.Repeat([]byte{0xBB}, 32),
		},
		Expires:  1234567890,
		Name:     "benchmark-lease",
		Alpn:     []string{"h2", "grpc", "http/1.1"},
		Metadata: "benchmark metadata",
	}

	b.ResetTimer()
	for range b.N {
		_, _ = msg.MarshalVT()
	}
}

// TestPacketType_String tests enum String method
func TestPacketType_String(t *testing.T) {
	tests := []struct {
		name string
		enum PacketType
		want string
	}{
		{"PACKET_TYPE_RELAY_INFO_REQUEST", PacketType_PACKET_TYPE_RELAY_INFO_REQUEST, "PACKET_TYPE_RELAY_INFO_REQUEST"},
		{"PACKET_TYPE_RELAY_INFO_RESPONSE", PacketType_PACKET_TYPE_RELAY_INFO_RESPONSE, "PACKET_TYPE_RELAY_INFO_RESPONSE"},
		{"PACKET_TYPE_LEASE_UPDATE_REQUEST", PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST, "PACKET_TYPE_LEASE_UPDATE_REQUEST"},
		{"PACKET_TYPE_LEASE_UPDATE_RESPONSE", PacketType_PACKET_TYPE_LEASE_UPDATE_RESPONSE, "PACKET_TYPE_LEASE_UPDATE_RESPONSE"},
		{"PACKET_TYPE_LEASE_DELETE_REQUEST", PacketType_PACKET_TYPE_LEASE_DELETE_REQUEST, "PACKET_TYPE_LEASE_DELETE_REQUEST"},
		{"PACKET_TYPE_LEASE_DELETE_RESPONSE", PacketType_PACKET_TYPE_LEASE_DELETE_RESPONSE, "PACKET_TYPE_LEASE_DELETE_RESPONSE"},
		{"PACKET_TYPE_CONNECTION_REQUEST", PacketType_PACKET_TYPE_CONNECTION_REQUEST, "PACKET_TYPE_CONNECTION_REQUEST"},
		{"PACKET_TYPE_CONNECTION_RESPONSE", PacketType_PACKET_TYPE_CONNECTION_RESPONSE, "PACKET_TYPE_CONNECTION_RESPONSE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.enum.String(); got != tt.want {
				t.Errorf("PacketType.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPacketType_Enum tests Enum method
func TestPacketType_Enum(t *testing.T) {
	if PacketType_PACKET_TYPE_CONNECTION_REQUEST.Enum() != nil && *PacketType_PACKET_TYPE_CONNECTION_REQUEST.Enum() != 6 {
		t.Error("PacketType.Enum() returned wrong value")
	}
	if PacketType_PACKET_TYPE_RELAY_INFO_REQUEST.Enum() != nil && *PacketType_PACKET_TYPE_RELAY_INFO_REQUEST.Enum() != 0 {
		t.Error("PACKET_TYPE_RELAY_INFO_REQUEST.Enum() should be 0")
	}
}

// TestResponseCode_String tests enum String method
func TestResponseCode_String(t *testing.T) {
	tests := []struct {
		name string
		enum ResponseCode
		want string
	}{
		{"RESPONSE_CODE_UNKNOWN", ResponseCode_RESPONSE_CODE_UNKNOWN, "RESPONSE_CODE_UNKNOWN"},
		{"RESPONSE_CODE_ACCEPTED", ResponseCode_RESPONSE_CODE_ACCEPTED, "RESPONSE_CODE_ACCEPTED"},
		{"RESPONSE_CODE_INVALID_EXPIRES", ResponseCode_RESPONSE_CODE_INVALID_EXPIRES, "RESPONSE_CODE_INVALID_EXPIRES"},
		{"RESPONSE_CODE_INVALID_IDENTITY", ResponseCode_RESPONSE_CODE_INVALID_IDENTITY, "RESPONSE_CODE_INVALID_IDENTITY"},
		{"RESPONSE_CODE_INVALID_NAME", ResponseCode_RESPONSE_CODE_INVALID_NAME, "RESPONSE_CODE_INVALID_NAME"},
		{"RESPONSE_CODE_INVALID_ALPN", ResponseCode_RESPONSE_CODE_INVALID_ALPN, "RESPONSE_CODE_INVALID_ALPN"},
		{"RESPONSE_CODE_REJECTED", ResponseCode_RESPONSE_CODE_REJECTED, "RESPONSE_CODE_REJECTED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.enum.String(); got != tt.want {
				t.Errorf("ResponseCode.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestResponseCode_Enum tests Enum method
func TestResponseCode_Enum(t *testing.T) {
	if ResponseCode_RESPONSE_CODE_ACCEPTED.Enum() != nil && *ResponseCode_RESPONSE_CODE_ACCEPTED.Enum() != 1 {
		t.Error("ResponseCode.Enum() returned wrong value")
	}
	if ResponseCode_RESPONSE_CODE_UNKNOWN.Enum() != nil && *ResponseCode_RESPONSE_CODE_UNKNOWN.Enum() != 0 {
		t.Error("RESPONSE_CODE_UNKNOWN.Enum() should be 0")
	}
}

// TestRelayInfo_Getters tests all getter methods
func TestRelayInfo_Getters(t *testing.T) {
	identity := &rdsec.Identity{Id: "relay-test", PublicKey: []byte{0xAA}}
	msg := &RelayInfo{
		Identity: identity,
		Address:  []string{"addr1:8080", "addr2:8080"},
		Leases:   []*Lease{{Name: "lease1"}},
	}

	if got := msg.GetIdentity(); got == nil || got.Id != "relay-test" {
		t.Errorf("GetIdentity() = %v, want Id='relay-test'", got)
	}
	if got := msg.GetAddress(); len(got) != 2 {
		t.Errorf("GetAddress() length = %v, want 2", len(got))
	}
	if got := msg.GetLeases(); len(got) != 1 {
		t.Errorf("GetLeases() length = %v, want 1", len(got))
	}

	// Test nil defaults
	empty := &RelayInfo{}
	if got := empty.GetIdentity(); got != nil {
		t.Errorf("empty GetIdentity() = %v, want nil", got)
	}
	if got := empty.GetAddress(); got != nil {
		t.Errorf("empty GetAddress() = %v, want nil", got)
	}
	if got := empty.GetLeases(); got != nil {
		t.Errorf("empty GetLeases() = %v, want nil", got)
	}
}

// TestRelayInfo_Reset tests Reset method
func TestRelayInfo_Reset(t *testing.T) {
	msg := &RelayInfo{
		Identity: &rdsec.Identity{Id: "test"},
		Address:  []string{"addr1"},
		Leases:   []*Lease{{Name: "lease1"}},
	}

	msg.Reset()

	if msg.Identity != nil {
		t.Error("Reset() did not clear Identity")
	}
	if msg.Address != nil {
		t.Error("Reset() did not clear Address")
	}
	if msg.Leases != nil {
		t.Error("Reset() did not clear Leases")
	}
}

// TestRelayInfoRequest_Reset tests Reset method
func TestRelayInfoRequest_Reset(t *testing.T) {
	msg := &RelayInfoRequest{}
	msg.Reset() // Should not panic
}

// TestRelayInfoResponse_Getters tests all getter methods
func TestRelayInfoResponse_Getters(t *testing.T) {
	relay := &RelayInfo{Identity: &rdsec.Identity{Id: "response-test"}}
	msg := &RelayInfoResponse{
		RelayInfo: relay,
	}

	if got := msg.GetRelayInfo(); got == nil || got.Identity.Id != "response-test" {
		t.Errorf("GetRelayInfo() = %v, want Id='response-test'", got)
	}

	// Test nil defaults
	empty := &RelayInfoResponse{}
	if got := empty.GetRelayInfo(); got != nil {
		t.Errorf("empty GetRelayInfo() = %v, want nil", got)
	}
}

// TestRelayInfoResponse_Reset tests Reset method
func TestRelayInfoResponse_Reset(t *testing.T) {
	msg := &RelayInfoResponse{
		RelayInfo: &RelayInfo{Identity: &rdsec.Identity{Id: "test"}},
	}

	msg.Reset()

	if msg.RelayInfo != nil {
		t.Error("Reset() did not clear RelayInfo")
	}
}

// TestLeaseUpdateRequest_Getters tests all getter methods
func TestLeaseUpdateRequest_Getters(t *testing.T) {
	lease := &Lease{Name: "update-lease"}
	msg := &LeaseUpdateRequest{
		Lease:     lease,
		Nonce:     []byte{0x01, 0x02},
		Timestamp: 1234567890,
	}

	if got := msg.GetLease(); got == nil || got.Name != "update-lease" {
		t.Errorf("GetLease() = %v, want Name='update-lease'", got)
	}
	if got := msg.GetNonce(); !bytes.Equal(got, []byte{0x01, 0x02}) {
		t.Errorf("GetNonce() = %v, want [1 2]", got)
	}
	if got := msg.GetTimestamp(); got != 1234567890 {
		t.Errorf("GetTimestamp() = %v, want 1234567890", got)
	}

	// Test nil defaults
	empty := &LeaseUpdateRequest{}
	if got := empty.GetLease(); got != nil {
		t.Errorf("empty GetLease() = %v, want nil", got)
	}
	if got := empty.GetNonce(); got != nil {
		t.Errorf("empty GetNonce() = %v, want nil", got)
	}
}

// TestLeaseUpdateRequest_Reset tests Reset method
func TestLeaseUpdateRequest_Reset(t *testing.T) {
	msg := &LeaseUpdateRequest{
		Lease:     &Lease{Name: "test"},
		Nonce:     []byte{0x01},
		Timestamp: 123,
	}

	msg.Reset()

	if msg.Lease != nil {
		t.Error("Reset() did not clear Lease")
	}
	if msg.Nonce != nil {
		t.Error("Reset() did not clear Nonce")
	}
	if msg.Timestamp != 0 {
		t.Error("Reset() did not clear Timestamp")
	}
}

// TestLeaseUpdateResponse_Reset tests Reset method
func TestLeaseUpdateResponse_Reset(t *testing.T) {
	msg := &LeaseUpdateResponse{
		Code: ResponseCode_RESPONSE_CODE_ACCEPTED,
	}

	msg.Reset()

	if msg.Code != ResponseCode_RESPONSE_CODE_UNKNOWN {
		t.Error("Reset() did not clear Code to default")
	}
}

// TestLeaseDeleteRequest_Reset tests Reset method
func TestLeaseDeleteRequest_Reset(t *testing.T) {
	msg := &LeaseDeleteRequest{
		Identity: &rdsec.Identity{Id: "delete-test"},
	}

	msg.Reset()

	if msg.Identity != nil {
		t.Error("Reset() did not clear Identity")
	}
}

// TestLeaseDeleteResponse_Reset tests Reset method
func TestLeaseDeleteResponse_Reset(t *testing.T) {
	msg := &LeaseDeleteResponse{
		Code: ResponseCode_RESPONSE_CODE_ACCEPTED,
	}

	msg.Reset()

	if msg.Code != ResponseCode_RESPONSE_CODE_UNKNOWN {
		t.Error("Reset() did not clear Code to default")
	}
}
