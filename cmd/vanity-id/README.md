# Vanity ID Generator

A high-performance parallel vanity ID generator that uses `cryptoops.DeriveID` and `randpool` to quickly generate cryptographic identities with custom prefix patterns.

## Features

- **Parallel Processing**: Uses multiple goroutines to maximize CPU utilization
- **Fast Random Generation**: Leverages `randpool.CSPRNG_RAND` for efficient random number generation
- **Real-time Statistics**: Displays attempt rate, progress, and estimated time every 2 seconds
- **Smart ETA Calculation**: Mathematically calculates expected completion time based on prefix length
- **Configurable**: Customizable prefix, worker count, and result limit

## Usage

```bash
# Generate one ID with "CHAT" prefix (default)
go run ./cmd/vanity-id

# Generate IDs with custom prefix
go run ./cmd/vanity-id -prefix PORTAL

# Generate multiple IDs
go run ./cmd/vanity-id -prefix DNS -max 3

# Use more workers (default is number of CPUs)
go run ./cmd/vanity-id -prefix KEY -workers 16

# Generate unlimited IDs (press Ctrl+C to stop)
go run ./cmd/vanity-id -prefix TEST -max 0
```

## Command-line Options

- `-prefix`: ID prefix to search for (default: "CHAT")
- `-workers`: Number of parallel workers (default: number of CPUs)
- `-max`: Maximum number of results to find, 0 = unlimited (default: 1)

## Output Example

```
Searching for IDs with prefix: TEST (4 characters)
Using 8 parallel workers
Max results: 1
Expected attempts per result: 524288 (average)

[Stats] Attempts: 546004 | Found: 0 | Rate: 272418/sec | Elapsed: 2.0s | ETA: 2s
[#1] Found at 3.60s (attempt #807217):
  ID:         TESTIWIBIRNDLZOHD3H2D6AD7Q
  PrivateKey: ZIhWbN39MThmbqREW+Ir7PvRxzzcuEVvJlOGwuive1ZL6RMsaBDcOSWj5MzSeyS+uqG8JARUssjODC70oC+sXg==
  PublicKey:  S+kTLGgQ3Dklo+TM0nskvrqhvCQEVLLIzgwu9KAvrF4=


=== Final Stats ===
Total attempts: 810086
Total found:    1
Elapsed time:   3.60s
Rate:           225067 attempts/sec
```

**Note**: Keys are displayed in base64 encoding for readability:
- PrivateKey: 64 bytes (ed25519 seed + public key)
- PublicKey: 32 bytes

## How It Works

1. **Random Key Generation**: Each worker generates random ed25519 private keys using `randpool.CSPRNG_RAND`
2. **ID Derivation**: The corresponding ID is derived using `cryptoops.DeriveID` which uses HMAC-SHA256 and base32 encoding
3. **Prefix Matching**: The ID is checked against the desired prefix
4. **ETA Calculation**: Expected completion time is calculated based on:
   - Current attempt rate (attempts/sec)
   - Remaining results needed
   - Mathematical probability (32^n for n character prefix)
5. **Result Collection**: Matching credentials are collected and displayed with their full private/public key pairs

## Performance Notes

- The search difficulty increases exponentially with prefix length
- Each additional character multiplies the expected attempts by ~32 (base32 alphabet size)
- Average attempts needed:
  - 1 character: ~16 attempts
  - 2 characters: ~512 attempts
  - 3 characters: ~16,384 attempts
  - 4 characters: ~524,288 attempts
  - 5 characters: ~16,777,216 attempts

On a typical 8-core CPU, you can expect:
- ~250,000-300,000 attempts/second
- 1-2 character prefixes: instant
- 3 character prefixes: < 1 second
- 4 character prefixes: 2-10 seconds
- 5 character prefixes: 1-5 minutes

## Integration

The generated credentials can be used with the `cryptoops.Credential` type:

```go
import (
    "crypto/ed25519"
    "encoding/base64"
    "gosuda.org/portal/portal/core/cryptoops"
)

// Use the private key from the output (base64 encoded)
privateKeyB64 := "ZIhWbN39MThmbqREW+Ir7PvRxzzcuEVvJlOGwuive1ZL6RMsaBDcOSWj5MzSeyS+uqG8JARUssjODC70oC+sXg=="
privateKeyBytes, err := base64.StdEncoding.DecodeString(privateKeyB64)
if err != nil {
    panic(err)
}

cred, err := cryptoops.NewCredentialFromPrivateKey(ed25519.PrivateKey(privateKeyBytes))
if err != nil {
    panic(err)
}

// Verify the ID matches
fmt.Println(cred.ID()) // Should print: TESTIWIBIRNDLZOHD3H2D6AD7Q
