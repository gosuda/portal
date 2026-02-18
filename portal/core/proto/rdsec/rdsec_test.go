package rdsec

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdentity_MarshalVT_UnmarshalVT tests round-trip serialization for Identity.
func TestIdentity_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *Identity
		wantErr bool
	}{
		{
			name:    "empty",
			input:   &Identity{},
			wantErr: false,
		},
		{
			name: "full",
			input: &Identity{
				Id:        "test-id-12345",
				PublicKey: []byte{0x01, 0x02, 0x03, 0x04, 0x05},
			},
			wantErr: false,
		},
		{
			name: "id only",
			input: &Identity{
				Id: "client-id",
			},
			wantErr: false,
		},
		{
			name: "public key only",
			input: &Identity{
				PublicKey: []byte{0xAA, 0xBB, 0xCC, 0xDD},
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

			got := &Identity{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestIdentity_CloneVT tests that CloneVT creates an independent copy.
func TestIdentity_CloneVT(t *testing.T) {
	original := &Identity{
		Id:        "original-id",
		PublicKey: []byte{0x01, 0x02, 0x03},
	}

	cloned := original.CloneVT()

	// Verify clone equals original
	assert.True(t, original.EqualVT(cloned), "clone does not equal original")

	// Modify clone
	cloned.Id = "modified-id"
	cloned.PublicKey[0] = 0xFF

	// Verify original unchanged
	assert.Equal(t, "original-id", original.Id)
	assert.Equal(t, byte(0x01), original.PublicKey[0])
}

// TestIdentity_EqualVT tests equality comparison.
func TestIdentity_EqualVT(t *testing.T) {
	tests := []struct {
		name string
		a    *Identity
		b    *Identity
		want bool
	}{
		{
			name: "both nil",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "same instance",
			a:    &Identity{Id: "test"},
			b:    &Identity{Id: "test"},
			want: true,
		},
		{
			name: "different id",
			a:    &Identity{Id: "test-a"},
			b:    &Identity{Id: "test-b"},
			want: false,
		},
		{
			name: "different public key",
			a:    &Identity{PublicKey: []byte{0x01}},
			b:    &Identity{PublicKey: []byte{0x02}},
			want: false,
		},
		{
			name: "one nil",
			a:    &Identity{Id: "test"},
			b:    nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.EqualVT(tt.b))
		})
	}
}

// TestIdentity_SizeVT tests size calculation.
func TestIdentity_SizeVT(t *testing.T) {
	msg := &Identity{
		Id:        "test-id",
		PublicKey: []byte{0x01, 0x02, 0x03},
	}

	size := msg.SizeVT()
	data, err := msg.MarshalVT()
	require.NoError(t, err)

	assert.Equal(t, len(data), size)
}

// TestClientInitPayload_MarshalVT_UnmarshalVT tests round-trip serialization.
func TestClientInitPayload_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *ClientInitPayload
		wantErr bool
	}{
		{
			name:    "empty",
			input:   &ClientInitPayload{},
			wantErr: false,
		},
		{
			name: "full",
			input: &ClientInitPayload{
				Version:          ProtocolVersion_PROTOCOL_VERSION_1,
				Nonce:            []byte{0x01, 0x02, 0x03, 0x04},
				Timestamp:        1234567890,
				Identity:         &Identity{Id: "client-id", PublicKey: []byte{0xAA, 0xBB}},
				Alpn:             "h2",
				SessionPublicKey: []byte{0x11, 0x22, 0x33, 0x44},
			},
			wantErr: false,
		},
		{
			name: "with identity only",
			input: &ClientInitPayload{
				Identity: &Identity{Id: "test-client"},
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

			got := &ClientInitPayload{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestClientInitPayload_CloneVT tests deep cloning with nested Identity.
func TestClientInitPayload_CloneVT(t *testing.T) {
	original := &ClientInitPayload{
		Version:   ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:     []byte{0x01, 0x02},
		Timestamp: 999,
		Identity:  &Identity{Id: "nested-id", PublicKey: []byte{0x03, 0x04}},
		Alpn:      "h2",
	}

	cloned := original.CloneVT()

	// Verify clone equals original
	assert.True(t, original.EqualVT(cloned), "clone does not equal original")

	// Modify nested identity in clone
	cloned.Identity.Id = "modified-nested"
	cloned.Identity.PublicKey[0] = 0xFF

	// Verify original nested identity unchanged
	assert.Equal(t, "nested-id", original.Identity.Id)
	assert.Equal(t, byte(0x03), original.Identity.PublicKey[0])
}

// TestServerInitPayload_MarshalVT_UnmarshalVT tests round-trip serialization.
func TestServerInitPayload_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *ServerInitPayload
		wantErr bool
	}{
		{
			name:    "empty",
			input:   &ServerInitPayload{},
			wantErr: false,
		},
		{
			name: "full",
			input: &ServerInitPayload{
				Version:          ProtocolVersion_PROTOCOL_VERSION_1,
				Nonce:            []byte{0x01, 0x02, 0x03, 0x04},
				Timestamp:        9876543210,
				Identity:         &Identity{Id: "server-id", PublicKey: []byte{0xAA, 0xBB}},
				Alpn:             "h2",
				SessionPublicKey: []byte{0x11, 0x22, 0x33, 0x44},
			},
			wantErr: false,
		},
		{
			name: "with identity only",
			input: &ServerInitPayload{
				Identity: &Identity{Id: "test-server"},
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

			got := &ServerInitPayload{}
			err = got.UnmarshalVT(data)
			require.NoError(t, err)

			assert.True(t, tt.input.EqualVT(got), "roundtrip mismatch")
		})
	}
}

// TestServerInitPayload_CloneVT tests deep cloning with nested Identity.
func TestServerInitPayload_CloneVT(t *testing.T) {
	original := &ServerInitPayload{
		Version:   ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:     []byte{0x01, 0x02},
		Timestamp: 888,
		Identity:  &Identity{Id: "server-nested", PublicKey: []byte{0x05, 0x06}},
		Alpn:      "h2",
	}

	cloned := original.CloneVT()

	// Verify clone equals original
	assert.True(t, original.EqualVT(cloned), "clone does not equal original")

	// Modify nested identity in clone
	cloned.Identity.Id = "modified-server"

	// Verify original nested identity unchanged
	assert.Equal(t, "server-nested", original.Identity.Id)
}

// TestReset tests that Reset clears all fields.
func TestReset(t *testing.T) {
	// Test Identity reset
	ident := &Identity{
		Id:        "test-id",
		PublicKey: []byte{0x01, 0x02},
	}
	ident.Reset()
	assert.Empty(t, ident.Id)
	assert.Nil(t, ident.PublicKey)

	// Test ClientInitPayload reset
	payload := &ClientInitPayload{
		Version:   ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:     []byte{0x01},
		Timestamp: 123,
		Identity:  &Identity{Id: "test"},
		Alpn:      "h2",
	}
	payload.Reset()
	assert.Zero(t, payload.Version)
	assert.Nil(t, payload.Nonce)
	assert.Zero(t, payload.Timestamp)
	assert.Nil(t, payload.Identity)
	assert.Empty(t, payload.Alpn)
}

// TestMarshalToSizedBufferVT tests buffer marshaling.
func TestMarshalToSizedBufferVT(t *testing.T) {
	msg := &Identity{
		Id:        "buffer-test",
		PublicKey: []byte{0x01, 0x02, 0x03},
	}

	size := msg.SizeVT()
	buf := make([]byte, size)

	n, err := msg.MarshalToSizedBufferVT(buf)
	require.NoError(t, err)
	assert.Equal(t, size, n)

	// Verify unmarshal works
	got := &Identity{}
	err = got.UnmarshalVT(buf[:n])
	require.NoError(t, err)

	assert.True(t, msg.EqualVT(got), "roundtrip mismatch with MarshalToSizedBufferVT")
}

// TestConcurrentSerialization tests concurrent marshal/unmarshal.
func TestConcurrentSerialization(t *testing.T) {
	msg := &ClientInitPayload{
		Version:          ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:            []byte{0x01, 0x02, 0x03, 0x04},
		Timestamp:        1234567890,
		Identity:         &Identity{Id: "concurrent-test", PublicKey: []byte{0xAA, 0xBB}},
		Alpn:             "h2",
		SessionPublicKey: []byte{0x11, 0x22, 0x33, 0x44},
	}

	data, err := msg.MarshalVT()
	require.NoError(t, err)

	// Run concurrent unmarshals
	done := make(chan bool, 10)
	for range 10 {
		go func() {
			got := &ClientInitPayload{}
			require.NoError(t, got.UnmarshalVT(data))
			assert.True(t, msg.EqualVT(got), "concurrent roundtrip mismatch")
			done <- true
		}()
	}

	for range 10 {
		<-done
	}
}

// TestProtoMessage tests ProtoMessage stub exists.
func TestProtoMessage(t *testing.T) {
	// These tests just verify the stub methods exist and don't panic
	var (
		ident      = &Identity{}
		clientInit = &ClientInitPayload{}
		serverInit = &ServerInitPayload{}
	)

	// Should not panic
	assert.NotPanics(t, func() { ident.ProtoMessage() })
	assert.NotPanics(t, func() { clientInit.ProtoMessage() })
	assert.NotPanics(t, func() { serverInit.ProtoMessage() })
}

// TestGetters tests getter methods.
func TestGetters(t *testing.T) {
	ident := &Identity{
		Id:        "test-id",
		PublicKey: []byte{0x01, 0x02},
	}

	assert.Equal(t, "test-id", ident.GetId())
	assert.Equal(t, []byte{0x01, 0x02}, ident.GetPublicKey())

	// Test nil case
	var nilIdent *Identity
	assert.Empty(t, nilIdent.GetId())
	assert.Nil(t, nilIdent.GetPublicKey())
}

// TestNilHandling tests nil message handling.
func TestNilHandling(t *testing.T) {
	var nilIdent *Identity

	// MarshalVT on nil should return nil, nil
	data, err := nilIdent.MarshalVT()
	assert.NoError(t, err)
	assert.Nil(t, data)

	// CloneVT on nil should return nil
	assert.Nil(t, nilIdent.CloneVT())

	// SizeVT on nil should return 0
	assert.Zero(t, nilIdent.SizeVT())

	// EqualVT on nil with nil should return true
	assert.True(t, nilIdent.EqualVT(nil))

	// EqualVT on nil with non-nil should return false
	assert.False(t, nilIdent.EqualVT(&Identity{}))

	// Test all message types handle nil correctly
	testCases := []struct {
		name string
		test func(t *testing.T)
	}{
		{"ClientInitPayload", func(t *testing.T) {
			var msg *ClientInitPayload
			data, err := msg.MarshalVT()
			assert.NoError(t, err)
			assert.Nil(t, data)
			assert.Nil(t, msg.CloneVT())
			assert.Zero(t, msg.SizeVT())
		}},
		{"ServerInitPayload", func(t *testing.T) {
			var msg *ServerInitPayload
			data, err := msg.MarshalVT()
			assert.NoError(t, err)
			assert.Nil(t, data)
			assert.Nil(t, msg.CloneVT())
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.test)
	}
}

// TestMarshalVT_Roundtrip tests marshal/unmarshal roundtrip.
func TestMarshalVT_Roundtrip(t *testing.T) {
	msg := &Identity{
		Id:        "roundtrip-test",
		PublicKey: []byte{0x01, 0x02, 0x03},
	}

	data, err := msg.MarshalVT()
	require.NoError(t, err)

	got := &Identity{}
	err = got.UnmarshalVT(data)
	require.NoError(t, err)

	assert.True(t, msg.EqualVT(got), "MarshalVT roundtrip mismatch")
}

// TestUnmarshalVTUnsafe tests unsafe unmarshaling.
func TestUnmarshalVTUnsafe(t *testing.T) {
	msg := &ClientInitPayload{
		Version:          ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:            []byte{0x01, 0x02, 0x03, 0x04},
		Timestamp:        1234567890,
		Identity:         &Identity{Id: "unsafe-test", PublicKey: []byte{0xAA, 0xBB}},
		Alpn:             "h2",
		SessionPublicKey: []byte{0x11, 0x22, 0x33, 0x44},
	}

	data, err := msg.MarshalVT()
	require.NoError(t, err)

	got := &ClientInitPayload{}
	err = got.UnmarshalVTUnsafe(data)
	require.NoError(t, err)

	assert.True(t, msg.EqualVT(got), "UnmarshalVTUnsafe roundtrip mismatch")
}

// BenchmarkIdentity_MarshalVT benchmarks marshaling.
func BenchmarkIdentity_MarshalVT(b *testing.B) {
	msg := &Identity{
		Id:        "benchmark-test-id-12345",
		PublicKey: bytes.Repeat([]byte{0xAA}, 32),
	}

	b.ResetTimer()
	for range b.N {
		_, _ = msg.MarshalVT()
	}
}

// BenchmarkIdentity_UnmarshalVT benchmarks unmarshaling.
func BenchmarkIdentity_UnmarshalVT(b *testing.B) {
	msg := &Identity{
		Id:        "benchmark-test-id-12345",
		PublicKey: bytes.Repeat([]byte{0xAA}, 32),
	}

	data, _ := msg.MarshalVT()

	b.ResetTimer()
	for range b.N {
		got := &Identity{}
		_ = got.UnmarshalVT(data)
	}
}

// BenchmarkClientInitPayload_MarshalVT benchmarks complex message marshaling.
func BenchmarkClientInitPayload_MarshalVT(b *testing.B) {
	msg := &ClientInitPayload{
		Version:          ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:            bytes.Repeat([]byte{0x01}, 32),
		Timestamp:        1234567890,
		Identity:         &Identity{Id: "benchmark-client", PublicKey: bytes.Repeat([]byte{0xAA}, 32)},
		Alpn:             "h2",
		SessionPublicKey: bytes.Repeat([]byte{0xFF}, 32),
	}

	b.ResetTimer()
	for range b.N {
		_, _ = msg.MarshalVT()
	}
}

// BenchmarkClientInitPayload_UnmarshalVT benchmarks complex message unmarshaling.
func BenchmarkClientInitPayload_UnmarshalVT(b *testing.B) {
	msg := &ClientInitPayload{
		Version:          ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:            bytes.Repeat([]byte{0x01}, 32),
		Timestamp:        1234567890,
		Identity:         &Identity{Id: "benchmark-client", PublicKey: bytes.Repeat([]byte{0xAA}, 32)},
		Alpn:             "h2",
		SessionPublicKey: bytes.Repeat([]byte{0xFF}, 32),
	}

	data, _ := msg.MarshalVT()

	b.ResetTimer()
	for range b.N {
		got := &ClientInitPayload{}
		_ = got.UnmarshalVT(data)
	}
}

// TestProtocolVersion_String tests enum String method.
func TestProtocolVersion_String(t *testing.T) {
	tests := []struct {
		name string
		enum ProtocolVersion
		want string
	}{
		{"PROTOCOL_VERSION_1", ProtocolVersion_PROTOCOL_VERSION_1, "PROTOCOL_VERSION_1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.enum.String())
		})
	}

	// Test that invalid value returns a non-empty string
	invalid := ProtocolVersion(999).String()
	assert.NotEmpty(t, invalid)
}

// TestProtocolVersion_Enum tests Enum method.
func TestProtocolVersion_Enum(t *testing.T) {
	if ProtocolVersion_PROTOCOL_VERSION_1.Enum() != nil {
		assert.Equal(t, ProtocolVersion_PROTOCOL_VERSION_1, *ProtocolVersion_PROTOCOL_VERSION_1.Enum())
	}
}

// TestClientInitPayload_Getters tests all getter methods.
func TestClientInitPayload_Getters(t *testing.T) {
	msg := &ClientInitPayload{
		Version:          ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:            []byte{0x01, 0x02, 0x03},
		Timestamp:        1234567890,
		Identity:         &Identity{Id: "getter-test", PublicKey: []byte{0xAA}},
		Alpn:             "h2",
		SessionPublicKey: []byte{0x11, 0x22},
	}

	assert.Equal(t, ProtocolVersion_PROTOCOL_VERSION_1, msg.GetVersion())
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, msg.GetNonce())
	assert.Equal(t, int64(1234567890), msg.GetTimestamp())
	assert.Equal(t, "getter-test", msg.GetIdentity().GetId())
	assert.Equal(t, "h2", msg.GetAlpn())
	assert.Equal(t, []byte{0x11, 0x22}, msg.GetSessionPublicKey())

	// Test nil defaults
	empty := &ClientInitPayload{}
	assert.Equal(t, ProtocolVersion_PROTOCOL_VERSION_1, empty.GetVersion())
	assert.Nil(t, empty.GetNonce())
	assert.Nil(t, empty.GetIdentity())
	assert.Empty(t, empty.GetAlpn())
}

// TestServerInitPayload_Getters tests all getter methods.
func TestServerInitPayload_Getters(t *testing.T) {
	msg := &ServerInitPayload{
		Version:          ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:            []byte{0x01, 0x02},
		Timestamp:        9876543210,
		Identity:         &Identity{Id: "server-test", PublicKey: []byte{}},
		Alpn:             "h3",
		SessionPublicKey: []byte{0xCC, 0xDD},
	}

	assert.Equal(t, ProtocolVersion_PROTOCOL_VERSION_1, msg.GetVersion())
	assert.Equal(t, []byte{0x01, 0x02}, msg.GetNonce())
	assert.Equal(t, int64(9876543210), msg.GetTimestamp())
	assert.Equal(t, "server-test", msg.GetIdentity().GetId())
	assert.Equal(t, "h3", msg.GetAlpn())
	assert.Equal(t, []byte{0xCC, 0xDD}, msg.GetSessionPublicKey())
}

// TestServerInitPayload_Reset tests Reset method.
func TestServerInitPayload_Reset(t *testing.T) {
	msg := &ServerInitPayload{
		Version:          ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:            []byte{0x01},
		Timestamp:        123,
		Identity:         &Identity{Id: "test"},
		Alpn:             "h2",
		SessionPublicKey: []byte{0xAA},
	}

	msg.Reset()

	assert.Equal(t, ProtocolVersion_PROTOCOL_VERSION_1, msg.Version)
	assert.Nil(t, msg.Nonce)
	assert.Zero(t, msg.Timestamp)
	assert.Nil(t, msg.Identity)
	assert.Empty(t, msg.Alpn)
	assert.Nil(t, msg.SessionPublicKey)
}
