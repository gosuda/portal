package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeTunnelScriptIncludesShellChecksumVerification(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/tunnel?os=linux", http.NoBody)
	rec := httptest.NewRecorder()

	serveTunnelScript(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("serveTunnelScript status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "CHECKSUM_URL=\"${BIN_URL}.sha256\"") {
		t.Fatalf("shell script missing checksum URL contract: %q", body)
	}
	if !strings.Contains(body, "sha256sum \"$BIN_PATH\"") {
		t.Fatalf("shell script missing sha256sum verification: %q", body)
	}
	if !strings.Contains(body, "fail-closed") {
		t.Fatalf("shell script missing fail-closed wording: %q", body)
	}
}

func TestServeTunnelScriptIncludesPowerShellChecksumVerification(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/tunnel?os=windows", http.NoBody)
	rec := httptest.NewRecorder()

	serveTunnelScript(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("serveTunnelScript status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "$ChecksumUrl = \"$BinUrl.sha256\"") {
		t.Fatalf("powershell script missing checksum URL contract: %q", body)
	}
	if !strings.Contains(body, "Get-FileHash -Algorithm SHA256 -Path $BinPath") {
		t.Fatalf("powershell script missing SHA256 verification: %q", body)
	}
	if !strings.Contains(body, "fail-closed") {
		t.Fatalf("powershell script missing fail-closed wording: %q", body)
	}
}

func TestServeTunnelBinaryServesChecksumSidecarAndHeader(t *testing.T) {
	originalAssetMap := tunnelBinaryAssetBySlug
	tunnelBinaryAssetBySlug = map[string]string{
		"linux-amd64": "dist/.gitkeep",
	}
	t.Cleanup(func() {
		tunnelBinaryAssetBySlug = originalAssetMap
	})

	binaryReq := httptest.NewRequest(http.MethodGet, "/tunnel/bin/linux-amd64", http.NoBody)
	binaryRec := httptest.NewRecorder()
	serveTunnelBinary(binaryRec, binaryReq)

	if binaryRec.Code != http.StatusOK {
		t.Fatalf("serveTunnelBinary status = %d, want %d", binaryRec.Code, http.StatusOK)
	}
	sum := sha256.Sum256(binaryRec.Body.Bytes())
	wantChecksum := hex.EncodeToString(sum[:])
	if got := binaryRec.Header().Get("X-Checksum-Sha256"); got != wantChecksum {
		t.Fatalf("binary checksum header = %q, want %q", got, wantChecksum)
	}

	checksumReq := httptest.NewRequest(http.MethodGet, "/tunnel/bin/linux-amd64.sha256", http.NoBody)
	checksumRec := httptest.NewRecorder()
	serveTunnelBinary(checksumRec, checksumReq)

	if checksumRec.Code != http.StatusOK {
		t.Fatalf("serveTunnelBinary checksum status = %d, want %d", checksumRec.Code, http.StatusOK)
	}
	checksumBody := strings.TrimSpace(checksumRec.Body.String())
	if !strings.HasPrefix(checksumBody, wantChecksum+"  portal-tunnel-linux-amd64") {
		t.Fatalf("checksum sidecar body = %q, want prefix %q", checksumBody, wantChecksum+"  portal-tunnel-linux-amd64")
	}
}

func TestServeTunnelBinaryUnknownChecksumSlugReturnsNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/tunnel/bin/not-a-slug.sha256", http.NoBody)
	rec := httptest.NewRecorder()

	serveTunnelBinary(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("serveTunnelBinary status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
