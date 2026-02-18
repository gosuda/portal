package rdverb

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rdsec "gosuda.org/portal/portal/core/proto/rdsec"
)

// TestPacket_MarshalVT_UnmarshalVT tests round-trip serialization for Packet.
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
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			got := &Packet{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestPacket_AllPacketTypes tests serialization of all packet types.
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
			require.NoError(t, err)

			got := &Packet{}
			require.NoError(t, got.UnmarshalVT(data))

			assert.True(t, msg.EqualVT(got), "roundtrip mismatch for %v", pt)
		})
	}
}

// TestRelayInfo_MarshalVT_UnmarshalVT tests round-trip serialization.
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
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			got := &RelayInfo{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestRelayInfo_WithMultipleLeases tests array handling for leases.
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
	require.NoError(t, err)

	got := &RelayInfo{}
	require.NoError(t, got.UnmarshalVT(data))

	assert.Len(t, got.Leases, len(leases))

	for i, want := range leases {
		assert.Equal(t, want.Name, got.Leases[i].Name, "lease[%d].Name", i)
	}
}

// TestLease_MarshalVT_UnmarshalVT tests round-trip serialization.
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
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			got := &Lease{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestLeaseUpdateRequest_MarshalVT_UnmarshalVT tests round-trip serialization.
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
				Lease: lease,
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
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			got := &LeaseUpdateRequest{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestResponseCode_AllValues tests all response code values.
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
			require.NoError(t, err)

			got := &LeaseUpdateResponse{}
			require.NoError(t, got.UnmarshalVT(data))

			assert.Equal(t, code, got.Code)
		})
	}
}

// TestLeaseDeleteRequest_MarshalVT_UnmarshalVT tests round-trip serialization.
func TestLeaseDeleteRequest_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *LeaseDeleteRequest
		wantErr bool
	}{
		{
			name: "full",
			input: &LeaseDeleteRequest{
				Identity: &rdsec.Identity{Id: "delete-id", PublicKey: []byte{0x01}},
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
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			got := &LeaseDeleteRequest{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestConnectionRequest_MarshalVT_UnmarshalVT tests round-trip serialization.
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
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			got := &ConnectionRequest{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestRelayInfoRequest_MarshalVT_UnmarshalVT tests empty message.
func TestRelayInfoRequest_MarshalVT_UnmarshalVT(t *testing.T) {
	msg := &RelayInfoRequest{}

	data, err := msg.MarshalVT()
	require.NoError(t, err)

	got := &RelayInfoRequest{}
	err = got.UnmarshalVT(data)
	require.NoError(t, err)

	assert.True(t, msg.EqualVT(got), "roundtrip mismatch for empty RelayInfoRequest")
}

// TestRelayInfoResponse_MarshalVT_UnmarshalVT tests with RelayInfo.
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
	require.NoError(t, err)

	got := &RelayInfoResponse{}
	err = got.UnmarshalVT(data)
	require.NoError(t, err)

	assert.True(t, msg.EqualVT(got), "roundtrip mismatch")
}

// TestCloneVT tests cloning creates independent copies.
func TestCloneVT(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		original := &Packet{
			Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
			Payload: []byte{0x01, 0x02, 0x03},
		}
		cloned := original.CloneVT()

		cloned.Type = PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST
		cloned.Payload[0] = 0xFF

		assert.Equal(t, PacketType_PACKET_TYPE_CONNECTION_REQUEST, original.Type)
		assert.Equal(t, byte(0x01), original.Payload[0])
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

		assert.Equal(t, "test", original.Identity.Id)
		assert.Equal(t, "addr1", original.Address[0])
		assert.Equal(t, "lease1", original.Leases[0].Name)
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

		assert.Equal(t, "lease-clone", original.Identity.Id)
		assert.Equal(t, int64(12345), original.Expires)
		assert.Equal(t, "clone-lease", original.Name)
		assert.Equal(t, "h2", original.Alpn[0])
	})
}

// TestEqualVT tests equality comparison.
func TestEqualVT(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		a := &Packet{Type: PacketType_PACKET_TYPE_CONNECTION_REQUEST, Payload: []byte{0x01}}
		b := &Packet{Type: PacketType_PACKET_TYPE_CONNECTION_REQUEST, Payload: []byte{0x01}}
		c := &Packet{Type: PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST, Payload: []byte{0x01}}

		assert.True(t, a.EqualVT(b), "Equal packets should be equal")
		assert.False(t, a.EqualVT(c), "Different packet types should not be equal")
		assert.False(t, a.EqualVT(nil), "Packet should not equal nil")
		assert.True(t, (*Packet)(nil).EqualVT(nil), "nil should equal nil")
	})

	t.Run("Lease", func(t *testing.T) {
		identity := &rdsec.Identity{Id: "test"}
		a := &Lease{Identity: identity, Expires: 123, Name: "test"}
		b := &Lease{Identity: identity, Expires: 123, Name: "test"}
		c := &Lease{Identity: identity, Expires: 456, Name: "test"}

		assert.True(t, a.EqualVT(b), "Equal leases should be equal")
		assert.False(t, a.EqualVT(c), "Leases with different Expires should not be equal")
	})
}

// TestSizeVT tests size calculation accuracy.
func TestSizeVT(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		msg := &Packet{
			Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
			Payload: []byte("test payload"),
		}

		size := msg.SizeVT()
		data, err := msg.MarshalVT()
		require.NoError(t, err)

		assert.Equal(t, len(data), size)
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
		require.NoError(t, err)

		assert.Equal(t, len(data), size)
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
		require.NoError(t, err)

		assert.Equal(t, len(data), size)
	})
}

// TestReset tests Reset clears all fields.
func TestReset(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		p := &Packet{Type: PacketType_PACKET_TYPE_CONNECTION_REQUEST, Payload: []byte{0x01}}
		p.Reset()
		assert.Equal(t, PacketType(0), p.Type)
		assert.Nil(t, p.Payload)
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
		assert.Nil(t, l.Identity)
		assert.Zero(t, l.Expires)
		assert.Empty(t, l.Name)
		assert.Nil(t, l.Alpn)
		assert.Empty(t, l.Metadata)
	})
}

// TestGetters tests getter methods.
func TestGetters(t *testing.T) {
	t.Run("Packet", func(t *testing.T) {
		p := &Packet{
			Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
			Payload: []byte{0x01, 0x02},
		}

		assert.Equal(t, PacketType_PACKET_TYPE_CONNECTION_REQUEST, p.GetType())
		assert.Equal(t, []byte{0x01, 0x02}, p.GetPayload())
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

		assert.Equal(t, identity, l.GetIdentity())
		assert.Equal(t, int64(12345), l.GetExpires())
		assert.Equal(t, "lease-name", l.GetName())
		assert.Len(t, l.GetAlpn(), 2)
		assert.Equal(t, "lease-metadata", l.GetMetadata())
	})

	t.Run("nil Lease", func(t *testing.T) {
		var l *Lease
		assert.Nil(t, l.GetIdentity())
		assert.Zero(t, l.GetExpires())
		assert.Empty(t, l.GetName())
		assert.Nil(t, l.GetAlpn())
		assert.Empty(t, l.GetMetadata())
	})
}

// TestNilHandling tests nil message handling.
func TestNilHandling(t *testing.T) {
	testCases := []struct {
		name string
		test func(t *testing.T)
	}{
		{"Packet", func(t *testing.T) {
			var msg *Packet
			data, err := msg.MarshalVT()
			assert.NoError(t, err)
			assert.Nil(t, data)
			assert.Nil(t, msg.CloneVT())
			assert.Zero(t, msg.SizeVT())
		}},
		{"RelayInfo", func(t *testing.T) {
			var msg *RelayInfo
			assert.Nil(t, msg.CloneVT())
		}},
		{"Lease", func(t *testing.T) {
			var msg *Lease
			assert.Nil(t, msg.CloneVT())
		}},
		{"LeaseUpdateRequest", func(t *testing.T) {
			var msg *LeaseUpdateRequest
			assert.Nil(t, msg.CloneVT())
		}},
		{"LeaseUpdateResponse", func(t *testing.T) {
			var msg *LeaseUpdateResponse
			assert.Nil(t, msg.CloneVT())
		}},
		{"LeaseDeleteRequest", func(t *testing.T) {
			var msg *LeaseDeleteRequest
			assert.Nil(t, msg.CloneVT())
		}},
		{"LeaseDeleteResponse", func(t *testing.T) {
			var msg *LeaseDeleteResponse
			assert.Nil(t, msg.CloneVT())
		}},
		{"ConnectionRequest", func(t *testing.T) {
			var msg *ConnectionRequest
			assert.Nil(t, msg.CloneVT())
		}},
		{"ConnectionResponse", func(t *testing.T) {
			var msg *ConnectionResponse
			assert.Nil(t, msg.CloneVT())
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.test)
	}
}

// TestMarshalVT_Roundtrip tests marshal/unmarshal roundtrip.
func TestMarshalVT_Roundtrip(t *testing.T) {
	msg := &Packet{
		Type:    PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: []byte{0x01, 0x02, 0x03},
	}

	data, err := msg.MarshalVT()
	require.NoError(t, err)

	got := &Packet{}
	err = got.UnmarshalVT(data)
	require.NoError(t, err)

	assert.True(t, msg.EqualVT(got), "MarshalVT roundtrip mismatch")
}

// TestUnmarshalVTUnsafe tests unsafe unmarshaling.
func TestUnmarshalVTUnsafe(t *testing.T) {
	msg := &ConnectionRequest{
		LeaseId:        "unsafe-test",
		ClientIdentity: &rdsec.Identity{Id: "unsafe-client", PublicKey: []byte{0xAA, 0xBB}},
	}

	data, err := msg.MarshalVT()
	require.NoError(t, err)

	got := &ConnectionRequest{}
	err = got.UnmarshalVTUnsafe(data)
	require.NoError(t, err)

	assert.True(t, msg.EqualVT(got), "UnmarshalVTUnsafe roundtrip mismatch")
}

// TestEmptyResponseMessages tests response messages.
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
			require.NoError(t, err)

			got := &LeaseUpdateResponse{}
			require.NoError(t, got.UnmarshalVT(data))

			assert.Equal(t, code, got.Code)
		})
	}
}

// TestComplexRelayInfo tests complex RelayInfo with multiple nested elements.
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
	require.NoError(t, err)

	got := &RelayInfo{}
	err = got.UnmarshalVT(data)
	require.NoError(t, err)

	assert.True(t, msg.EqualVT(got), "complex RelayInfo roundtrip mismatch")

	// Verify all fields
	assert.Len(t, got.Address, 3)
	assert.Len(t, got.Leases, 2)
}

// BenchmarkPacket_MarshalVT benchmarks packet marshaling.
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

// BenchmarkPacket_UnmarshalVT benchmarks packet unmarshaling.
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

// BenchmarkRelayInfo_MarshalVT benchmarks complex relay info marshaling.
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

// BenchmarkLease_MarshalVT benchmarks lease marshaling.
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

// TestPacketType_String tests enum String method.
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
			assert.Equal(t, tt.want, tt.enum.String())
		})
	}
}

// TestPacketType_Enum tests Enum method.
func TestPacketType_Enum(t *testing.T) {
	if PacketType_PACKET_TYPE_CONNECTION_REQUEST.Enum() != nil {
		assert.Equal(t, PacketType_PACKET_TYPE_CONNECTION_REQUEST, *PacketType_PACKET_TYPE_CONNECTION_REQUEST.Enum())
	}
	if PacketType_PACKET_TYPE_RELAY_INFO_REQUEST.Enum() != nil {
		assert.Equal(t, PacketType_PACKET_TYPE_RELAY_INFO_REQUEST, *PacketType_PACKET_TYPE_RELAY_INFO_REQUEST.Enum())
	}
}

// TestResponseCode_String tests enum String method.
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
			assert.Equal(t, tt.want, tt.enum.String())
		})
	}
}

// TestResponseCode_Enum tests Enum method.
func TestResponseCode_Enum(t *testing.T) {
	if ResponseCode_RESPONSE_CODE_ACCEPTED.Enum() != nil {
		assert.Equal(t, ResponseCode_RESPONSE_CODE_ACCEPTED, *ResponseCode_RESPONSE_CODE_ACCEPTED.Enum())
	}
	if ResponseCode_RESPONSE_CODE_UNKNOWN.Enum() != nil {
		assert.Equal(t, ResponseCode_RESPONSE_CODE_UNKNOWN, *ResponseCode_RESPONSE_CODE_UNKNOWN.Enum())
	}
}

// TestRelayInfo_Getters tests all getter methods.
func TestRelayInfo_Getters(t *testing.T) {
	identity := &rdsec.Identity{Id: "relay-test", PublicKey: []byte{0xAA}}
	msg := &RelayInfo{
		Identity: identity,
		Address:  []string{"addr1:8080", "addr2:8080"},
		Leases:   []*Lease{{Name: "lease1"}},
	}

	assert.Equal(t, identity, msg.GetIdentity())
	assert.Len(t, msg.GetAddress(), 2)
	assert.Len(t, msg.GetLeases(), 1)

	// Test nil defaults
	empty := &RelayInfo{}
	assert.Nil(t, empty.GetIdentity())
	assert.Nil(t, empty.GetAddress())
	assert.Nil(t, empty.GetLeases())
}

// TestRelayInfo_Reset tests Reset method.
func TestRelayInfo_Reset(t *testing.T) {
	msg := &RelayInfo{
		Identity: &rdsec.Identity{Id: "test"},
		Address:  []string{"addr1"},
		Leases:   []*Lease{{Name: "lease1"}},
	}

	msg.Reset()

	assert.Nil(t, msg.Identity)
	assert.Nil(t, msg.Address)
	assert.Nil(t, msg.Leases)
}

// TestRelayInfoRequest_Reset tests Reset method.
func TestRelayInfoRequest_Reset(t *testing.T) {
	msg := &RelayInfoRequest{}
	msg.Reset() // Should not panic
}

// TestRelayInfoResponse_Getters tests all getter methods.
func TestRelayInfoResponse_Getters(t *testing.T) {
	relay := &RelayInfo{Identity: &rdsec.Identity{Id: "response-test"}}
	msg := &RelayInfoResponse{
		RelayInfo: relay,
	}

	assert.Equal(t, relay, msg.GetRelayInfo())

	// Test nil defaults
	empty := &RelayInfoResponse{}
	assert.Nil(t, empty.GetRelayInfo())
}

// TestRelayInfoResponse_Reset tests Reset method.
func TestRelayInfoResponse_Reset(t *testing.T) {
	msg := &RelayInfoResponse{
		RelayInfo: &RelayInfo{Identity: &rdsec.Identity{Id: "test"}},
	}

	msg.Reset()

	assert.Nil(t, msg.RelayInfo)
}

// TestLeaseUpdateRequest_Getters tests all getter methods.
func TestLeaseUpdateRequest_Getters(t *testing.T) {
	lease := &Lease{Name: "update-lease"}
	msg := &LeaseUpdateRequest{
		Lease: lease,
	}

	assert.Equal(t, lease, msg.GetLease())
	// Test nil defaults
	empty := &LeaseUpdateRequest{}
	assert.Nil(t, empty.GetLease())
}

// TestLeaseUpdateRequest_Reset tests Reset method.
func TestLeaseUpdateRequest_Reset(t *testing.T) {
	msg := &LeaseUpdateRequest{
		Lease: &Lease{Name: "test"},
	}

	msg.Reset()

	assert.Nil(t, msg.Lease)
}

// TestLeaseUpdateResponse_Reset tests Reset method.
func TestLeaseUpdateResponse_Reset(t *testing.T) {
	msg := &LeaseUpdateResponse{
		Code: ResponseCode_RESPONSE_CODE_ACCEPTED,
	}

	msg.Reset()

	assert.Equal(t, ResponseCode_RESPONSE_CODE_UNKNOWN, msg.Code)
}

// TestLeaseDeleteRequest_Reset tests Reset method.
func TestLeaseDeleteRequest_Reset(t *testing.T) {
	msg := &LeaseDeleteRequest{
		Identity: &rdsec.Identity{Id: "delete-test"},
	}

	msg.Reset()

	assert.Nil(t, msg.Identity)
}

// TestLeaseDeleteResponse_Reset tests Reset method.
func TestLeaseDeleteResponse_Reset(t *testing.T) {
	msg := &LeaseDeleteResponse{
		Code: ResponseCode_RESPONSE_CODE_ACCEPTED,
	}

	msg.Reset()

	assert.Equal(t, ResponseCode_RESPONSE_CODE_UNKNOWN, msg.Code)
}
