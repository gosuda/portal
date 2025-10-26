# RelayServer 상세 동작 문서

## 개요

RelayServer는 DNS 기반 P2P 프록시 레이어로, libp2p 위에 구축된 경량 서비스입니다. 이 서버는 클라이언트 간의 안전한 연결을 중개하고, 리스(lease) 관리, 암호화된 통신, 다양한 프로토콜 지원 등의 기능을 제공합니다.

## 핵심 구성 요소

### 1. RelayServer 구조체

RelayServer는 시스템의 핵심 구조체로, 다음과 같은 주요 필드를 포함합니다:

```go
type RelayServer struct {
    credential *cryptoops.Credential  // 암호화 자격 증명
    identity   *rdsec.Identity        // 서버 신원 정보
    address    []string               // 서버 주소 목록

    connidCounter   int64              // 연결 ID 카운터
    connections     map[int64]*Connection // 활성 연결 맵
    connectionsLock sync.RWMutex       // 연결 맵 락

    leaseConnections     map[string]*Connection // 리스-연결 맵
    leaseConnectionsLock sync.RWMutex           // 리스-연결 맵 락

    relayedConnections     map[string][]*yamux.Stream // 릴레이된 연결 맵
    relayedConnectionsLock sync.RWMutex                // 릴레이된 연결 맵 락

    leaseManager *LeaseManager // 리스 관리자

    stopch    chan struct{} // 종료 신호 채널
    waitgroup sync.WaitGroup // 대기 그룹
}
```

### 2. Connection 구조체

각 클라이언트 연결을 나타내는 구조체입니다:

```go
type Connection struct {
    conn io.ReadWriteCloser    // 기본 연결
    sess *yamux.Session        // 다중화 세션

    streams     map[uint32]*yamux.Stream // 스트림 맵
    streamsLock sync.Mutex                // 스트림 맵 락
}
```

### 3. LeaseManager

리스(임대) 정보를 관리하는 컴포넌트로, 만료된 리스를 정리하고 리스 수명을 관리합니다:

```go
type LeaseManager struct {
    leases      map[string]*LeaseEntry // 리스 맵 (키: 신원 ID)
    leasesLock  sync.RWMutex           // 리스 맵 락
    stopCh      chan struct{}          // 종료 신호 채널
    ttlInterval time.Duration          // TTL 확인 간격
}
```

## 주요 동작

### 1. 서버 초기화

서버는 `NewRelayServer` 함수로 초기화됩니다:

```go
func NewRelayServer(credential *cryptoops.Credential, address []string) *RelayServer
```

- 암호화 자격 증명과 주소 목록을 받아 RelayServer 인스턴스를 생성
- 내부 맵들과 리스 관리자를 초기화
- 30초 간격의 TTL 확인을 위한 리스 관리자 생성

### 2. 연결 처리

#### 2.1 새 연결 수락

`HandleConnection` 메서드는 새로운 클라이언트 연결을 처리합니다:

1. yamux 서버 세션을 생성
2. 고유 연결 ID를 할당하고 연결 맵에 저장
3. `handleConn` 고루틴을 시작하여 연결을 비동기적으로 처리

#### 2.2 연결 관리

`handleConn` 메서드는 연결의 수명 주기를 관리합니다:

1. 세션에서 들어오는 스트림을 계속해서 수락
2. 각 스트림에 대해 `handleStream` 고루틴을 시작
3. 연결이 종료될 때 관련 리스를 정리하고 자원을 해제

### 3. 스트림 처리

`handleStream` 메서드는 개별 스트림의 요청을 처리합니다:

1. 스트림에서 패킷을 읽음
2. 패킷 타입에 따라 적절한 핸들러로 분기:
   - `PACKET_TYPE_RELAY_INFO_REQUEST`: `handleRelayInfoRequest`
   - `PACKET_TYPE_LEASE_UPDATE_REQUEST`: `handleLeaseUpdateRequest`
   - `PACKET_TYPE_LEASE_DELETE_REQUEST`: `handleLeaseDeleteRequest`
   - `PACKET_TYPE_CONNECTION_REQUEST`: `handleConnectionRequest`

### 4. 리스 관리

#### 4.1 리스 업데이트

`handleLeaseUpdateRequest` 메서드는 리스 업데이트 요청을 처리합니다:

1. 서명된 페이로드를 검증
2. 리스 만료 시간을 확인
3. 유효한 경우 리스를 업데이트하고 연결 맵에 등록
4. 응답 코드를 반환

#### 4.2 리스 삭제

`handleLeaseDeleteRequest` 메서드는 리스 삭제 요청을 처리합니다:

1. 서명된 페이로드를 검증
2. 해당 리스를 리스 관리자에서 삭제
3. 리스-연결 맵에서도 제거
4. 응답 코드를 반환

#### 4.3 리스 만료 관리

LeaseManager는 백그라운드에서 만료된 리스를 정리합니다:

1. `ttlWorker` 고루틴이 주기적으로 실행
2. 만료된 리스를 자동으로 제거
3. 특정 연결 ID와 관련된 모든 리스를 정리하는 기능 제공

### 5. 연결 릴레이

#### 5.1 연결 요청 처리

`handleConnectionRequest` 메서드는 클라이언트 간의 연결 요청을 처리합니다:

1. 대상 클라이언트의 리스가 존재하는지 확인
2. 리스 소유자에게 연결 요청을 전달
3. 요청이 수락되면 양방향 포워딩을 설정

#### 5.2 양방향 포워딩 설정

`setupBidirectionalForwarding` 메서드는 클라이언트 간의 데이터 포워딩을 설정합니다:

1. 리스 소유자에게 새 데이터 스트림을 열고 초기화
2. 클라이언트와 리스 소유자 간의 양방향 데이터 복사를 설정
3. 연결이 종료될 때 관련 자원을 정리

### 6. 암호화 및 보안

#### 6.1 핸드셰이크 프로토콜

X25519-ChaCha20Poly1305 기반의 핸드셰이크 프로토콜을 사용합니다:

1. 클라이언트와 서버가 각각 임시 키 쌍 생성
2. Ed25519로 서명된 초기화 메시지 교환
3. 공유 비밀을 계산하고 세션 키를 파생
4. ChaCha20Poly1305 AEAD 암호화 설정

#### 6.2 서명 검증

모든 인증된 요청은 Ed25519 서명을 통해 검증됩니다:

1. `VerifySignedPayload` 함수로 서명 검증
2. 신원 정보의 유효성 확인
3. 타임스탬프 검증으로 리플레이 공격 방지

### 7. 프로토콜 메시지

#### 7.1 패킷 구조

모든 통신은 다음과 같은 패킷 구조를 사용합니다:

```protobuf
message Packet {
  PacketType type = 1;  // 패킷 타입
  bytes payload = 2;    // 페이로드
}
```

#### 7.2 패킷 타입

- `PACKET_TYPE_RELAY_INFO_REQUEST/RESPONSE`: 릴레이 서버 정보 요청
- `PACKET_TYPE_LEASE_UPDATE_REQUEST/RESPONSE`: 리스 업데이트 요청
- `PACKET_TYPE_LEASE_DELETE_REQUEST/RESPONSE`: 리스 삭제 요청
- `PACKET_TYPE_CONNECTION_REQUEST/RESPONSE`: 연결 요청

#### 7.3 응답 코드

- `RESPONSE_CODE_ACCEPTED`: 요청 수락
- `RESPONSE_CODE_INVALID_EXPIRES`: 유효하지 않은 만료 시간
- `RESPONSE_CODE_INVALID_IDENTITY`: 유효하지 않은 신원
- `RESPONSE_CODE_INVALID_NAME`: 유효하지 않은 이름
- `RESPONSE_CODE_INVALID_ALPN`: 유효하지 않은 ALPN
- `RESPONSE_CODE_REJECTED`: 요청 거부

### 8. HTTP 관리 인터페이스

#### 8.1 관리 UI

웹 기반 관리 인터페이스를 제공합니다:

1. `/`: 서버 상태와 연결된 클라이언트 목록 표시
2. `/peer/{peerID}/`: 특정 피어로의 HTTP 프록시
3. `/hosts`: 연결된 호스트 목록 (JSON)
4. `/health`: 서버 상태 확인 (JSON)

#### 8.2 TCP 프록시

선택적으로 원시 TCP 연결을 지원합니다:

1. 지정된 포트에서 수신 대기
2. 들어오는 연결을 선택된 피어로 프록시
3. SSH 등의 TCP 기반 프로토콜 지원

### 9. 서버 수명 주기

#### 9.1 시작

1. `Start` 메서드로 리스 관리자 시작
2. 백그라운드 TTL 확인 고루틴 시작

#### 9.2 종료

1. `Stop` 메서드로 종료 신호 전송
2. 리스 관리자 중지
3. 모든 백그라운드 고루틴이 완료될 때까지 대기

## 동작 흐름 예시

### 1. 클라이언트 연결 및 리스 등록

1. 클라이언트가 RelayServer에 연결
2. yamux 세션이 설정되고 연결이 등록됨
3. 클라이언트가 리스 업데이트 요청을 보냄
4. 서버가 서명을 검증하고 리스를 등록
5. 리스-연결 맵이 업데이트됨

### 2. 클라이언트 간 연결 설정

1. 클라이언트 A가 클라이언트 B로의 연결을 요청
2. 서버가 클라이언트 B의 리스를 확인
3. 서버가 클라이언트 B에게 연결 요청을 전달
4. 클라이언트 B가 요청을 수락
5. 서버가 양방향 포워딩을 설정
6. 클라이언트 A와 B가 데이터를 교환

### 3. 연결 종료 및 정리

1. 클라이언트 연결이 종료됨
2. 관련 리스가 자동으로 정리됨
3. 릴레이된 연결이 모두 닫힘
4. 연결 맵에서 항목이 제거됨

## 보안 고려사항

1. **암호화**: 모든 통신은 X25519-ChaCha20Poly1305로 암호화됨
2. **인증**: Ed25519 서명으로 모든 요청의 출처를 검증
3. **리플레이 방지**: 타임스탬프 검증으로 오래된 메시지 거부
4. **신원 검증**: 공개 키에서 파생된 ID와 제공된 ID 일치 확인
5. **세션 키**: 각 연결마다 고유한 세션 키를 사용

## 암호화 상세

### 1. 자격 증명(Credential) 시스템

#### 1.1 자격 증명 생성

```go
type Credential struct {
    privateKey ed25519.PrivateKey  // Ed25519 개인 키
    publicKey  ed25519.PublicKey   // Ed25519 공개 키
    id         string              // 파생된 ID
}
```

- `NewCredential()`: 새로운 Ed25519 키 쌍을 생성하고 자격 증명을 반환
- `NewCredentialFromPrivateKey()`: 기존 개인 키로부터 자격 증명 생성

#### 1.2 ID 파생

```go
func DeriveID(publickey ed25519.PublicKey) string
```

- HMAC-SHA256을 사용하여 공개 키로부터 고유 ID를 파생
- 매직 문자열 "RDVERB_PROTOCOL_VER_01_SHA256_ID" 사용
- Base32 인코딩으로 최종 ID 생성

#### 1.3 서명 및 검증

- `Sign(data []byte)`: Ed25519로 데이터에 서명
- `Verify(data, sig []byte)`: Ed25519 서명 검증
- 모든 인증된 메시지는 이 서명 메커니즘을 통해 검증됨

### 2. 핸드셰이크 프로토콜 상세

#### 2.1 클라이언트 핸드셰이크

1. **임시 키 생성**: X25519 임시 키 쌍 생성
2. **ClientInitPayload 생성**:
   - 프로토콜 버전, nonce, 타임스탬프
   - 클라이언트 신원 정보
   - ALPN(Application-Layer Protocol Negotiation) 문자열
   - 임시 공개 키
3. **서명 및 전송**: 페이로드를 서명하여 서버로 전송
4. **서버 응답 수신**: ServerInitPayload 수신 및 검증
5. **세션 키 파생**: 클라이언트 암호화/복호화 키 파생
6. **SecureConnection 생성**: 암호화된 연결 설정

#### 2.2 서버 핸드셰이크

1. **클라이언트 요청 수신**: ClientInitPayload 수신
2. **클라이언트 검증**:
   - 프로토콜 버전 확인
   - 타임스탬프 유효성 검증 (30초 이내)
   - ALPN 일치 확인
   - 신원 정보 유효성 검증
   - Ed25519 서명 검증
3. **임시 키 생성**: X25519 임시 키 쌍 생성
4. **ServerInitPayload 생성**: 클라이언트와 유사한 구조로 서버 정보 생성
5. **세션 키 파생**: 서버 암호화/복호화 키 파생
6. **응답 전송 및 SecureConnection 생성**

#### 2.3 세션 키 파생

```go
// 클라이언트 측
clientEncryptKey, clientDecryptKey, err := h.deriveClientSessionKeys(
    clientPriv, clientPub, serverPub, clientNonce, serverNonce)

// 서버 측
serverEncryptKey, serverDecryptKey, err := h.deriveServerSessionKeys(
    serverPriv, serverPub, clientPub, clientNonce, serverNonce)
```

- X25519를 사용하여 공유 비밀 계산
- HKDF-SHA256을 사용하여 세션 키 파생
- 클라이언트 암호화 키 = 서버 복호화 키
- 서버 암호화 키 = 클라이언트 복호화 키
- 솔트(salt)로 nonce 조합 사용:
  - 클라이언트 암호화: `clientNonce + serverNonce`
  - 서버 암호화: `serverNonce + clientNonce`
- 정보 문자열(info): "RDSEC_KEY_CLIENT" / "RDSEC_KEY_SERVER"

#### 2.4 암호화된 통신

```go
type SecureConnection struct {
    conn         io.ReadWriteCloser  // 기본 연결
    encryptor    cipher.AEAD         // 암호화기 (ChaCha20Poly1305)
    decryptor    cipher.AEAD         // 복호화기 (ChaCha20Poly1305)
    encryptNonce []byte              // 암호화용 nonce
    decryptNonce []byte              // 복호화용 nonce
}
```

- 각 메시지마다 nonce 증가 (간단한 바이트 단위 증가)
- 길이 접두사가 있는 메시지 형식 사용:
  ```
  [4바이트 길이][데이터]
  ```
- 최대 패킷 크기: 64MB
- EncryptedData protobuf 메시지로 암호화된 데이터 전송:
  ```protobuf
  message EncryptedData {
    bytes nonce = 1;    // 12바이트 nonce
    bytes payload = 2;  // 암호화된 페이로드
  }
  ```

#### 2.5 길이 접두사 메시지 처리

핸드셰이크와 암호화된 통신에서 사용되는 공통 메시지 형식:

```go
// 쓰기: [4바이트 길이][데이터]
func writeLengthPrefixed(conn io.Writer, data []byte) error

// 읽기: [4바이트 길이][데이터]
func readLengthPrefixed(conn io.Reader) ([]byte, error)
```

- 빅 엔디안 형식으로 길이 인코딩
- 패킷 크기 제한으로 메모리 과사용 방지
- 전송 계층에서의 메시지 경계 명확화

### 3. WebSocket 스트림 지원

#### 3.1 wsStream 구조체

```go
type wsStream struct {
    c             *websocket.Conn  // WebSocket 연결
    currentReader io.Reader        // 현재 읽기 리더
}
```

#### 3.2 WebSocket 스트림 동작

1. **읽기 동작**:
   - WebSocket 메시지 리더를 가져옴
   - 데이터를 버퍼로 읽음
   - EOF 발생 시 다음 메시지를 위해 리더 재설정

2. **쓰기 동작**:
   - 바이너리 메시지 타입으로 데이터 전송
   - WebSocket 프레이밍 자동 처리

3. **닫기 동작**:
   - WebSocket 연결 종료

## 프로토콜 상세

### 1. 패킷 형식

모든 통신은 다음과 같은 구조를 따릅니다:

```
[4바이트 길이][패킷 데이터]
```

패킷 데이터는 protobuf로 직렬화된 `Packet` 메시지:

```protobuf
message Packet {
  PacketType type = 1;  // 패킷 타입
  bytes payload = 2;    // 페이로드
}
```

### 2. 메시지 타입별 상세

#### 2.1 릴레이 정보 요청/응답

- **요청**: `RelayInfoRequest` (빈 메시지)
- **응답**: `RelayInfoResponse`
  ```protobuf
  message RelayInfo {
    rdsec.Identity identity = 1;      // 서버 신원
    repeated string address = 2;      // 서버 주소 목록
    repeated string leases = 3;       // 활성 리스 ID 목록
  }
  ```

#### 2.2 리스 업데이트 요청/응답

- **요청**: `LeaseUpdateRequest` (SignedPayload로 래핑)
  ```protobuf
  message LeaseUpdateRequest {
    Lease lease = 1;        // 리스 정보
    bytes nonce = 2;        // nonce
    int64 timestamp = 3;    // 타임스탬프
  }
  
  message Lease {
    rdsec.Identity identity = 1;  // 클라이언트 신원
    int64 expires = 2;            // 만료 시간 (Unix 타임스탬프)
    string name = 3;               // 리스 이름
    repeated string alpn = 4;      // 지원 ALPN 목록
  }
  ```

- **응답**: `LeaseUpdateResponse`
  ```protobuf
  message LeaseUpdateResponse {
    ResponseCode code = 1;  // 응답 코드
  }
  ```

#### 2.3 리스 삭제 요청/응답

- **요청**: `LeaseDeleteRequest` (SignedPayload로 래핑)
  ```protobuf
  message LeaseDeleteRequest {
    rdsec.Identity identity = 1;  // 삭제할 신원
    bytes nonce = 2;               // nonce
    int64 timestamp = 3;           // 타임스탬프
  }
  ```

- **응답**: `LeaseDeleteResponse`
  ```protobuf
  message LeaseDeleteResponse {
    ResponseCode code = 1;  // 응답 코드
  }
  ```

#### 2.4 연결 요청/응답

- **요청**: `ConnectionRequest`
  ```protobuf
  message ConnectionRequest {
    string lease_id = 1;           // 대상 리스 ID
    rdsec.Identity client_identity = 2;  // 클라이언트 신원
  }
  ```

- **응답**: `ConnectionResponse`
  ```protobuf
  message ConnectionResponse {
    ResponseCode code = 1;  // 응답 코드
  }
  ```

## 성능 최적화

1. **다중화**: yamux를 사용한 단일 연결 위의 다중 스트림
2. **동시성**: 각 스트림과 연결을 독립적인 고루틴으로 처리
3. **버퍼 풀**: bytebufferpool을 사용한 메모리 할당 최적화
4. **락 최적화**: 읽기/쓰기 락을 사용한 동시 접근 최적화
5. **암호화 최적화**: ChaCha20Poly1305를 사용한 고성능 암호화
6. **네트워크 최적화**: 길이 접두사가 있는 바이너리 프로토콜 사용

## 확장성 고려사항

1. **프로토콜 버전 관리**: protobuf를 사용한 안전한 프로토콜 진화
2. **ALPN 지원**: 다양한 애플리케이션 프로토콜 지원
3. **플러그인 아키텍처**: 핸들러 기반의 확장 가능한 요청 처리
4. **모듈형 설계**: 암호화, 리스 관리, 연결 관리의 분리

이 문서는 RelayServer의 핵심 동작과 구조를 상세하게 설명하며, 시스템의 이해와 유지보수에 도움을 제공합니다.