module github.com/gosuda/portal/v2

go 1.26.1

require (
	github.com/aws/aws-sdk-go-v2 v1.41.1
	github.com/aws/aws-sdk-go-v2/config v1.32.8
	github.com/aws/aws-sdk-go-v2/credentials v1.19.8
	github.com/aws/aws-sdk-go-v2/service/route53 v1.62.1
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.0.1
	github.com/go-acme/lego/v4 v4.32.0
	github.com/gosuda/keyless_tls v0.0.1-0.20260304212324-7733f8366abc
	github.com/quic-go/quic-go v0.59.0
	github.com/rs/zerolog v1.34.0
	golang.org/x/crypto v0.48.0
	golang.org/x/net v0.50.0
	golang.org/x/sync v0.19.0
	golang.zx2c4.com/wireguard v0.0.0-20250521234502-f333402bd9cb
)

require (
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.14 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.6 // indirect
	github.com/aws/smithy-go v1.24.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/go-jose/go-jose/v4 v4.1.3 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/miekg/dns v1.1.72 // indirect
	golang.org/x/mod v0.32.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.org/x/tools v0.41.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c // indirect
)

exclude golang.zx2c4.com/wireguard/tun/netstack v0.0.0-20220703234212-c31a7b1ab478
