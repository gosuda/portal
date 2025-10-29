// 1. WASM 실행 환경 임포트
// (CDN을 사용하거나 로컬 경로를 사용할 수 있습니다)
// const wasm_exec_URL = "https://cdn.jsdelivr.net/gh/golang/go@go1.19/misc/wasm/wasm_exec.js";
const wasm_exec_URL = "/wasm_exec.js"; 
importScripts(wasm_exec_URL);

// --- 전역 상수 및 변수 ---

const wasm_URL = "/main.wasm";
// importScripts와 경로 일치
const CACHE_NAME = "WASM_Cache_v1";

// WASM 로딩 상태를 관리하기 위한 Promise (중복 로드 방지)
let wasmReadyPromise = null;

/**
 * Go WASM을 로드하고 실행합니다.
 */
async function runWASM() {
    const go = new Go();
    const cache = await caches.open(CACHE_NAME);
    let wasm_file;

    const cache_wasm = await cache.match(wasm_URL);
    
    if (cache_wasm) {
        console.log("Service Worker: 캐시에서 WASM 로드 중...");
        wasm_file = await cache_wasm.arrayBuffer();
    } else {
        console.warn("Service Worker: 캐시에 WASM이 없습니다. 네트워크에서 가져옵니다...");
        const resp = await fetch(wasm_URL);
        wasm_file = await resp.arrayBuffer();
        await cache.put(wasm_URL, new Response(wasm_file.slice(0)));
    }

    console.log("Service Worker: WebAssembly 인스턴스화...");
    const { instance } = await WebAssembly.instantiate(wasm_file, go.importObject);

    // go.run()은 Go의 main()을 실행하고, 
    // _relaydns_http 콜백이 등록되면 리턴합니다.
    go.run(instance);
    console.log("Service Worker: Go WASM 실행 완료. _relaydns_http가 준비되었습니다.");
}

/**
 * runWASM()이 한 번만 실행되도록 보장하는 래퍼 함수입니다.
 * @returns {Promise<void>} WASM이 준비되면 resolve되는 Promise
 */
function getWasmReady() {
    if (!wasmReadyPromise) {
        console.log("Service Worker: WASM 로딩 시작...");
        wasmReadyPromise = runWASM().catch(err => {
            console.error("Service Worker: WASM 실행 실패:", err);
            wasmReadyPromise = null; // 실패 시 다음 요청에서 재시도 허용
            throw err; // 에러를 호출자(fetch 핸들러)에게 전파
        });
    }
    return wasmReadyPromise;
}


// --- 1. 설치 (Install) 이벤트 리스너 ---
self.addEventListener('install', (event) => {
  console.log('Service Worker: 설치 중...');
  
  event.waitUntil(
    (async () => {
      const cache = await caches.open(CACHE_NAME);
      console.log('Service Worker: 필수 에셋 캐싱 중...');
      await cache.addAll([
        wasm_URL,
        wasm_exec_URL,
      ]);
      await self.skipWaiting();
    })()
  );
});

// --- 2. 활성화 (Activate) 이벤트 리스너 ---
self.addEventListener('activate', (event) => {
  console.log('Service Worker: 활성화 됨.');

  event.waitUntil(
    (async () => {
      await self.clients.claim();
      // WASM을 미리 로드하여 다음 fetch 요청에 대비
      console.log('Service Worker: Go WASM 선제적 로딩 시작...');
      await getWasmReady();
      console.log('Service Worker: Go WASM 선제적 로딩 완료.');
    })()
  );
});


// --- 3. 페치 (Fetch) 이벤트 리스너 ---
// 모든 요청을 Go 핸들러로 전달합니다.
self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  console.log(`Service Worker: Go 핸들러로 요청 전달: ${url.pathname}`);
  
  event.respondWith((async () => {
    try {
      // WASM이 준비될 때까지 기다림
      await getWasmReady(); 

      if (typeof _relaydns_http !== 'undefined') {
        // WASM이 준비되었고 핸들러 함수가 존재함
        const resp = await _relaydns_http(event.request);
        return resp;
      } else {
        // getWasmReady()가 성공했는데도 함수가 없는 비정상 상황
        console.error("Service Worker: WASM 로드는 성공했으나 _relaydns_http가 정의되지 않았습니다.");
        return new Response("WASM 핸들러를 사용할 수 없습니다.", { status: 500 });
      }
    } catch (err) {
      // 1. getWasmReady() 실패 (WASM 로드/실행 실패)
      // 2. _relaydns_http(event.request) 실패 (Go 핸들러 내부 에러)
      console.error(`Service Worker: Go 핸들러 처리 실패 (네트워크로 폴백): ${err}`, event.request.url);
      
      // WASM 핸들러 실패 시 네트워크로 폴백
      return fetch(event.request);
    }
  })());
});