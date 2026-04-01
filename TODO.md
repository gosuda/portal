# Portal Project TODO

이 문서는 Portal 프로젝트의 완성도를 높이기 위해 개선이 필요한 사항들을 정리한 로드맵입니다.

## 1. Core Relay & Network (Go)
- [ ] **전역 상태 제거 (Refactoring Global State)**: `portal/relay.go`의 `_yamux_config` 및 각종 제한 수치들을 전역 변수에서 `RelayServer` 구조체의 필드로 이동하여 테스트 용이성 및 다중 인스턴스 지원 강화.
- [ ] **정밀한 에러 핸들링 및 텔레메트리**: 릴레이 스트림 핸들러(`handleStream`)에서 발생하는 조용한 종료(Silent termination)를 방지하기 위해 상세한 로그 기록 및 프로메테우스(Prometheus) 메트릭 통합.
- [ ] **트래픽 셰이핑(Traffic Shaping) 통합**: 현재 분리되어 있는 BPS 관리 로직을 QoS(Quality of Service) 레이어로 통합하여 고부하 리스(Lease)에 대한 성능 최적화.

## 2. 데이터 지속성 및 관리 (Persistence)
- [ ] **리스 상태 저장소 구현**: `LeaseManager`의 데이터를 메모리(`map`)에서 SQLite 또는 Redis로 저장할 수 있는 인터페이스 구현. 서버 재시작 시 기존 리스 및 차단(Ban) 목록 복구 가능하게 개선.
- [ ] **리스 만료 로직 개선**: `cleanupExpiredLeases` 워커의 효율성을 높이고, 데이터베이스 기반의 만료 처리를 지원하도록 수정.

## 3. 웹 클라이언트 및 WASM (WebClient)
- [ ] **WebSocket 폴리필 완성**: `cmd/webclient/polyfill.js`가 네이티브 WebSocket의 모든 이벤트 핸들러(`onXXX`) 및 `readyState` 전이 상태를 완벽하게 모방하도록 개선.
- [ ] **프레임 파싱 고도화**: 현재 구현된 웹소켓 프레임 파싱 로직의 엣지 케이스(대용량 페이로드, 복합 제어 프레임 등) 검증 및 안정화.
- [ ] **WASM 바이너리 최적화**: 브라우저 로딩 속도 향상을 위해 WASM 파일 크기 최적화 및 캐싱 전략 수립.

## 4. 관리자 대시보드 및 모니터링 (Admin UI)
- [ ] **실시간 메트릭 표시**: 관리자 대시보드에서 리스별 실시간 트래픽(BPS), 활성 스트림 수, 업타임(Uptime) 정보를 실제 데이터 기반으로 표시 (`admin.go`의 TODO 해결).
- [ ] **고급 필터링 및 검색**: 대시보드에서 태그, 리스 생성일, 트래픽 양 등 다양한 조건으로 서버를 검색하고 필터링하는 기능 강화.
- [ ] **대량 작업(Bulk Actions) 확장**: 선택된 여러 리스에 대해 일괄적으로 정책을 적용하거나 메시지를 전송하는 기능 추가.

## 5. 벤치마크 및 성능 (Performance)
- [ ] **자동화된 프로파일 분석**: `bench-reporter`에서 CPU/Memory pprof 데이터를 분석하여 성능 병목 지점을 자동으로 감지하고 보고서에 포함하는 기능 구현.
- [ ] **E2EE 오버헤드 측정**: 엔드투엔드 암호화가 전체 처리량(Throughput)과 지연시간(Latency)에 미치는 영향을 상세히 분석하는 벤치마크 케이스 추가.

## 6. 문서화 및 개발자 경험 (Documentation & DX)
- [ ] **문제 해결 가이드 (Troubleshooting Guide)**: E2EE 핸드셰이크 실패, 터널 연결 끊김 등 흔히 발생하는 문제들에 대한 진단 및 해결 방법 문서화.
- [ ] **로컬 예제 코드 추가**: `README.md`에서 언급된 외부 저장소 대신 프로젝트 내부에 직접 실행 가능한 예제(Sample apps) 코드 배치.
- [ ] **설정 관리 유연화**: 환경 변수(Env vars)와 설정 파일을 통한 릴레이 서버 구성 기능을 강화하여 배포 편의성 증대.
