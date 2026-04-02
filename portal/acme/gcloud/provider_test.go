package gcloud

import (
	"testing"

	gdns "google.golang.org/api/dns/v1"
)

func TestDNSKeyDSRecordPrefersSHA256(t *testing.T) {
	t.Parallel()

	record, ok := dnsKeyDSRecord(&gdns.DnsKey{
		Algorithm: "ecdsap256sha256",
		KeyTag:    12345,
		Type:      "keySigning",
		IsActive:  true,
		Digests: []*gdns.DnsKeyDigest{
			{Type: "sha1", Digest: "AAAA"},
			{Type: "sha256", Digest: "BBBB"},
		},
	})
	if !ok {
		t.Fatal("dnsKeyDSRecord() = !ok, want ok")
	}
	if record != "12345 13 2 BBBB" {
		t.Fatalf("dnsKeyDSRecord() = %q, want %q", record, "12345 13 2 BBBB")
	}
}

func TestDNSSECStatusFromZoneUsesActiveKeySigningKey(t *testing.T) {
	t.Parallel()

	status := dnssecStatusFromZone(&gdns.ManagedZone{
		DnssecConfig: &gdns.ManagedZoneDnsSecConfig{State: "on"},
	}, []*gdns.DnsKey{
		{
			Algorithm: "rsasha256",
			KeyTag:    100,
			Type:      "zoneSigning",
			IsActive:  true,
			Digests: []*gdns.DnsKeyDigest{
				{Type: "sha256", Digest: "IGNORE"},
			},
		},
		{
			Algorithm: "rsasha256",
			KeyTag:    200,
			Type:      "keySigning",
			IsActive:  true,
			Digests: []*gdns.DnsKeyDigest{
				{Type: "sha256", Digest: "USEME"},
			},
		},
	})

	if status.State != "on" {
		t.Fatalf("dnssecStatusFromZone().State = %q, want %q", status.State, "on")
	}
	if status.DSRecord != "200 8 2 USEME" {
		t.Fatalf("dnssecStatusFromZone().DSRecord = %q, want %q", status.DSRecord, "200 8 2 USEME")
	}
}
