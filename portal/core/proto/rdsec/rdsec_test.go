package rdsec

import (
	"bytes"
	"testing"
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
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &Identity{}
			err = got.UnmarshalVT(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.input.EqualVT(got) {
				t.Errorf("roundtrip mismatch: got %+v, want %+v", got, tt.input)
			}
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
	if !original.EqualVT(cloned) {
		t.Error("clone does not equal original")
	}

	// Modify clone
	cloned.Id = "modified-id"
	cloned.PublicKey[0] = 0xFF

	// Verify original unchanged
	if original.Id != "original-id" {
		t.Error("original.Id was modified")
	}
	if original.PublicKey[0] != 0x01 {
		t.Error("original.PublicKey was modified")
	}
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
			if got := tt.a.EqualVT(tt.b); got != tt.want {
				t.Errorf("EqualVT() = %v, want %v", got, tt.want)
			}
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
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	if size != len(data) {
		t.Errorf("SizeVT() = %v, but MarshalVT() produced %v bytes", size, len(data))
	}
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
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &ClientInitPayload{}
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
	if !original.EqualVT(cloned) {
		t.Error("clone does not equal original")
	}

	// Modify nested identity in clone
	cloned.Identity.Id = "modified-nested"
	cloned.Identity.PublicKey[0] = 0xFF

	// Verify original nested identity unchanged
	if original.Identity.Id != "nested-id" {
		t.Error("original.Identity.Id was modified")
	}
	if original.Identity.PublicKey[0] != 0x03 {
		t.Error("original.Identity.PublicKey was modified")
	}
}

// TestSignedPayload_MarshalVT_UnmarshalVT tests round-trip serialization.
func TestSignedPayload_MarshalVT_UnmarshalVT(t *testing.T) {
	tests := []struct {
		name    string
		input   *SignedPayload
		wantErr bool
	}{
		{
			name:    "empty",
			input:   &SignedPayload{},
			wantErr: false,
		},
		{
			name: "full",
			input: &SignedPayload{
				Data:      []byte{0x01, 0x02, 0x03, 0x04, 0x05},
				Signature: []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE},
			},
			wantErr: false,
		},
		{
			name: "data only",
			input: &SignedPayload{
				Data: []byte("payload data"),
			},
			wantErr: false,
		},
		{
			name: "signature only",
			input: &SignedPayload{
				Signature: []byte{0xFF, 0xFF, 0xFF},
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

			got := &SignedPayload{}
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

// TestSignedPayload_CloneVT tests independent copy creation.
func TestSignedPayload_CloneVT(t *testing.T) {
	original := &SignedPayload{
		Data:      []byte{0x01, 0x02, 0x03},
		Signature: []byte{0xAA, 0xBB, 0xCC},
	}

	cloned := original.CloneVT()

	// Modify clone
	cloned.Data[0] = 0xFF
	cloned.Signature[0] = 0x00

	// Verify original unchanged
	if original.Data[0] != 0x01 {
		t.Error("original.Data was modified")
	}
	if original.Signature[0] != 0xAA {
		t.Error("original.Signature was modified")
	}
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
			if (err != nil) != tt.wantErr {
				t.Errorf("MarshalVT() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := &ServerInitPayload{}
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
	if !original.EqualVT(cloned) {
		t.Error("clone does not equal original")
	}

	// Modify nested identity in clone
	cloned.Identity.Id = "modified-server"

	// Verify original nested identity unchanged
	if original.Identity.Id != "server-nested" {
		t.Error("original.Identity.Id was modified")
	}
}

// TestReset tests that Reset clears all fields.
func TestReset(t *testing.T) {
	// Test Identity reset
	ident := &Identity{
		Id:        "test-id",
		PublicKey: []byte{0x01, 0x02},
	}
	ident.Reset()
	if ident.Id != "" {
		t.Error("Identity.Id not cleared after Reset()")
	}
	if ident.PublicKey != nil {
		t.Error("Identity.PublicKey not cleared after Reset()")
	}

	// Test ClientInitPayload reset
	payload := &ClientInitPayload{
		Version:   ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:     []byte{0x01},
		Timestamp: 123,
		Identity:  &Identity{Id: "test"},
		Alpn:      "h2",
	}
	payload.Reset()
	if payload.Version != 0 {
		t.Error("ClientInitPayload.Version not cleared after Reset()")
	}
	if payload.Nonce != nil {
		t.Error("ClientInitPayload.Nonce not cleared after Reset()")
	}
	if payload.Timestamp != 0 {
		t.Error("ClientInitPayload.Timestamp not cleared after Reset()")
	}
	if payload.Identity != nil {
		t.Error("ClientInitPayload.Identity not cleared after Reset()")
	}
	if payload.Alpn != "" {
		t.Error("ClientInitPayload.Alpn not cleared after Reset()")
	}
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
	if err != nil {
		t.Fatalf("MarshalToSizedBufferVT() error = %v", err)
	}

	if n != size {
		t.Errorf("MarshalToSizedBufferVT() returned %v, want %v", n, size)
	}

	// Verify unmarshal works
	got := &Identity{}
	err = got.UnmarshalVT(buf[:n])
	if err != nil {
		t.Fatalf("UnmarshalVT() error = %v", err)
	}

	if !msg.EqualVT(got) {
		t.Error("roundtrip mismatch with MarshalToSizedBufferVT")
	}
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
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	// Run concurrent unmarshals
	done := make(chan bool, 10)
	for range 10 {
		go func() {
			got := &ClientInitPayload{}
			if err := got.UnmarshalVT(data); err != nil {
				t.Errorf("concurrent UnmarshalVT() error = %v", err)
			}
			if !msg.EqualVT(got) {
				t.Error("concurrent roundtrip mismatch")
			}
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
		ident         = &Identity{}
		clientInit    = &ClientInitPayload{}
		signedPayload = &SignedPayload{}
		serverInit    = &ServerInitPayload{}
	)

	// Should not panic
	ident.ProtoMessage()
	clientInit.ProtoMessage()
	signedPayload.ProtoMessage()
	serverInit.ProtoMessage()
}

// TestGetters tests getter methods.
func TestGetters(t *testing.T) {
	ident := &Identity{
		Id:        "test-id",
		PublicKey: []byte{0x01, 0x02},
	}

	if got := ident.GetId(); got != "test-id" {
		t.Errorf("GetId() = %v, want test-id", got)
	}
	if got := ident.GetPublicKey(); len(got) != 2 || got[0] != 0x01 {
		t.Errorf("GetPublicKey() = %v, want [0x01, 0x02]", got)
	}

	// Test nil case
	var nilIdent *Identity
	if got := nilIdent.GetId(); got != "" {
		t.Errorf("GetId() on nil = %v, want empty string", got)
	}
	if got := nilIdent.GetPublicKey(); got != nil {
		t.Errorf("GetPublicKey() on nil = %v, want nil", got)
	}
}

// TestNilHandling tests nil message handling.
func TestNilHandling(t *testing.T) {
	var nilIdent *Identity

	// MarshalVT on nil should return nil, nil
	if data, err := nilIdent.MarshalVT(); err != nil || data != nil {
		t.Errorf("MarshalVT() on nil = (%v, %v), want (nil, nil)", data, err)
	}

	// CloneVT on nil should return nil
	if cloned := nilIdent.CloneVT(); cloned != nil {
		t.Errorf("CloneVT() on nil = %v, want nil", cloned)
	}

	// SizeVT on nil should return 0
	if size := nilIdent.SizeVT(); size != 0 {
		t.Errorf("SizeVT() on nil = %v, want 0", size)
	}

	// EqualVT on nil with nil should return true
	if !nilIdent.EqualVT(nil) {
		t.Error("EqualVT(nil, nil) = false, want true")
	}

	// EqualVT on nil with non-nil should return false
	if nilIdent.EqualVT(&Identity{}) {
		t.Error("EqualVT(nil, &Identity{}) = true, want false")
	}

	// Test all message types handle nil correctly
	testCases := []struct {
		name string
		test func() // test function that verifies nil handling
	}{
		{"ClientInitPayload", func() {
			var msg *ClientInitPayload
			if data, err := msg.MarshalVT(); err != nil || data != nil {
				t.Errorf("MarshalVT() on nil ClientInitPayload = (%v, %v), want (nil, nil)", data, err)
			}
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil ClientInitPayload should return nil")
			}
			if msg.SizeVT() != 0 {
				t.Error("SizeVT() on nil ClientInitPayload should return 0")
			}
		}},
		{"SignedPayload", func() {
			var msg *SignedPayload
			if data, err := msg.MarshalVT(); err != nil || data != nil {
				t.Errorf("MarshalVT() on nil SignedPayload = (%v, %v), want (nil, nil)", data, err)
			}
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil SignedPayload should return nil")
			}
		}},
		{"ServerInitPayload", func() {
			var msg *ServerInitPayload
			if data, err := msg.MarshalVT(); err != nil || data != nil {
				t.Errorf("MarshalVT() on nil ServerInitPayload = (%v, %v), want (nil, nil)", data, err)
			}
			if msg.CloneVT() != nil {
				t.Error("CloneVT() on nil ServerInitPayload should return nil")
			}
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.test()
		})
	}
}

// TestMarshalVT_Roundtrip tests marshal/unmarshal roundtrip.
func TestMarshalVT_Roundtrip(t *testing.T) {
	msg := &Identity{
		Id:        "roundtrip-test",
		PublicKey: []byte{0x01, 0x02, 0x03},
	}

	data, err := msg.MarshalVT()
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	got := &Identity{}
	err = got.UnmarshalVT(data)
	if err != nil {
		t.Fatalf("UnmarshalVT() error = %v", err)
	}

	if !msg.EqualVT(got) {
		t.Error("MarshalVT roundtrip mismatch")
	}
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
	if err != nil {
		t.Fatalf("MarshalVT() error = %v", err)
	}

	got := &ClientInitPayload{}
	err = got.UnmarshalVTUnsafe(data)
	if err != nil {
		t.Fatalf("UnmarshalVTUnsafe() error = %v", err)
	}

	if !msg.EqualVT(got) {
		t.Error("UnmarshalVTUnsafe roundtrip mismatch")
	}
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
			if got := tt.enum.String(); got != tt.want {
				t.Errorf("ProtocolVersion.String() = %v, want %v", got, tt.want)
			}
		})
	}

	// Test that invalid value returns a non-empty string
	invalid := ProtocolVersion(999).String()
	if invalid == "" {
		t.Error("ProtocolVersion(999).String() should return non-empty string")
	}
}

// TestProtocolVersion_Enum tests Enum method.
func TestProtocolVersion_Enum(t *testing.T) {
	if ProtocolVersion_PROTOCOL_VERSION_1.Enum() != nil && *ProtocolVersion_PROTOCOL_VERSION_1.Enum() != 0 {
		t.Error("ProtocolVersion.Enum() should return 0")
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

	if got := msg.GetVersion(); got != ProtocolVersion_PROTOCOL_VERSION_1 {
		t.Errorf("GetVersion() = %v, want %v", got, ProtocolVersion_PROTOCOL_VERSION_1)
	}
	if got := msg.GetNonce(); !bytes.Equal(got, []byte{0x01, 0x02, 0x03}) {
		t.Errorf("GetNonce() = %v, want [1 2 3]", got)
	}
	if got := msg.GetTimestamp(); got != 1234567890 {
		t.Errorf("GetTimestamp() = %v, want 1234567890", got)
	}
	if got := msg.GetIdentity(); got == nil || got.Id != "getter-test" {
		t.Errorf("GetIdentity() = %v, want Id='getter-test'", got)
	}
	if got := msg.GetAlpn(); got != "h2" {
		t.Errorf("GetAlpn() = %v, want h2", got)
	}
	if got := msg.GetSessionPublicKey(); !bytes.Equal(got, []byte{0x11, 0x22}) {
		t.Errorf("GetSessionPublicKey() = %v, want [17 34]", got)
	}

	// Test nil defaults
	empty := &ClientInitPayload{}
	if got := empty.GetVersion(); got != ProtocolVersion_PROTOCOL_VERSION_1 {
		t.Errorf("empty GetVersion() should return default")
	}
	if got := empty.GetNonce(); got != nil {
		t.Errorf("empty GetNonce() = %v, want nil", got)
	}
	if got := empty.GetIdentity(); got != nil {
		t.Errorf("empty GetIdentity() = %v, want nil", got)
	}
	if got := empty.GetAlpn(); got != "" {
		t.Errorf("empty GetAlpn() = %v, want empty string", got)
	}
}

// TestSignedPayload_Getters tests all getter methods.
func TestSignedPayload_Getters(t *testing.T) {
	msg := &SignedPayload{
		Data:      []byte{0x01, 0x02, 0x03},
		Signature: []byte{0xAA, 0xBB},
	}

	if got := msg.GetData(); !bytes.Equal(got, []byte{0x01, 0x02, 0x03}) {
		t.Errorf("GetData() = %v, want [1 2 3]", got)
	}
	if got := msg.GetSignature(); !bytes.Equal(got, []byte{0xAA, 0xBB}) {
		t.Errorf("GetSignature() = %v, want [170 187]", got)
	}

	// Test nil defaults
	empty := &SignedPayload{}
	if got := empty.GetData(); got != nil {
		t.Errorf("empty GetData() = %v, want nil", got)
	}
	if got := empty.GetSignature(); got != nil {
		t.Errorf("empty GetSignature() = %v, want nil", got)
	}
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

	if got := msg.GetVersion(); got != ProtocolVersion_PROTOCOL_VERSION_1 {
		t.Errorf("GetVersion() = %v, want %v", got, ProtocolVersion_PROTOCOL_VERSION_1)
	}
	if got := msg.GetNonce(); !bytes.Equal(got, []byte{0x01, 0x02}) {
		t.Errorf("GetNonce() = %v, want [1 2]", got)
	}
	if got := msg.GetTimestamp(); got != 9876543210 {
		t.Errorf("GetTimestamp() = %v, want 9876543210", got)
	}
	if got := msg.GetIdentity(); got == nil || got.Id != "server-test" {
		t.Errorf("GetIdentity() = %v, want Id='server-test'", got)
	}
	if got := msg.GetAlpn(); got != "h3" {
		t.Errorf("GetAlpn() = %v, want h3", got)
	}
	if got := msg.GetSessionPublicKey(); !bytes.Equal(got, []byte{0xCC, 0xDD}) {
		t.Errorf("GetSessionPublicKey() = %v, want [204 221]", got)
	}
}

// TestSignedPayload_Reset tests Reset method.
func TestSignedPayload_Reset(t *testing.T) {
	msg := &SignedPayload{
		Data:      []byte{0x01, 0x02},
		Signature: []byte{0xAA, 0xBB},
	}

	msg.Reset()

	if msg.Data != nil {
		t.Error("Reset() did not clear Data")
	}
	if msg.Signature != nil {
		t.Error("Reset() did not clear Signature")
	}
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

	if msg.Version != ProtocolVersion_PROTOCOL_VERSION_1 {
		t.Error("Reset() changed Version from default")
	}
	if msg.Nonce != nil {
		t.Error("Reset() did not clear Nonce")
	}
	if msg.Timestamp != 0 {
		t.Error("Reset() did not clear Timestamp")
	}
	if msg.Identity != nil {
		t.Error("Reset() did not clear Identity")
	}
	if msg.Alpn != "" {
		t.Error("Reset() did not clear Alpn")
	}
	if msg.SessionPublicKey != nil {
		t.Error("Reset() did not clear SessionPublicKey")
	}
}
