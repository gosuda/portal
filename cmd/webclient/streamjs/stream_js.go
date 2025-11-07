package streamjs

import (
	"io"
	"sync"
	"syscall/js"
)

var (
	_ReadableStream = js.Global().Get("ReadableStream")
	_Object         = js.Global().Get("Object")
	_Promise        = js.Global().Get("Promise")
	_Error          = js.Global().Get("Error")
	_Uint8Array     = js.Global().Get("Uint8Array")
)

type ReadableStream struct {
	js.Value
	r         io.ReadCloser
	closeOnce sync.Once

	// 데이터를 읽기 위한 버퍼
	buffer []byte

	funcsToBeReleased []js.Func
}

// NewReadableStream는 Go의 io.ReadCloser를 JS ReadableStream으로 래핑합니다.
func NewReadableStream(r io.ReadCloser) *ReadableStream {
	// 1. Go 래퍼 구조체를 먼저 생성합니다.
	rs := &ReadableStream{
		r:      r,
		buffer: make([]byte, 4096), // 4KB 버퍼로 초기화
	}

	// 2. JS 콜백 함수들을 정의합니다. 이 함수들은 'rs' 포인터를 클로저로 캡처합니다.
	var onStart, onPull, onCancel js.Func

	// start: 스트림이 시작될 때 호출됨 (보통 비워둠)
	onStart = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		// controller := args[0]
		return nil
	})

	// pull: JS 런타임이 데이터를 요청할 때 호출됨 (가장 중요)
	onPull = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		controller := args[0]

		// 3. Promise를 생성하여 반환합니다. (비동기 작업)
		//    JS 스레드를 차단하지 않기 위해 Go 루틴에서 실제 I/O를 수행합니다.
		var promiseFn js.Func
		promiseFn = js.FuncOf(func(this js.Value, pArgs []js.Value) interface{} {
			resolve := pArgs[0]
			reject := pArgs[1]

			// 4. 고루틴에서 (잠재적으로 블로킹되는) Read 수행
			go func() {
				defer promiseFn.Release()

				n, err := rs.r.Read(rs.buffer)

				// 5. 에러 처리
				if err != nil {
					if err == io.EOF {
						// 5a. 파일 끝 (EOF) -> 스트림 정상 종료
						controller.Call("close")
					} else {
						// 5b. 실제 읽기 오류 -> 스트림 에러 종료
						jsErr := _Error.New(err.Error())
						controller.Call("error", jsErr)
						reject.Invoke(jsErr) // Promise 거부
					}
					resolve.Invoke() // Promise 이행 (pull 작업 완료)
					return
				}

				// 6. 성공적으로 데이터를 읽은 경우
				if n > 0 {
					// 6a. 읽은 만큼(n 바이트) JS Uint8Array 생성
					jsChunk := _Uint8Array.New(n)

					// 6b. Go 버퍼(rs.buffer[:n])에서 JS Uint8Array로 바이트 복사
					js.CopyBytesToJS(jsChunk, rs.buffer[:n])

					// 6c. JS 스트림 컨트롤러에 데이터 추가 (enqueue)
					controller.Call("enqueue", jsChunk)
				}

				// 7. pull 작업이 성공적으로 완료되었음을 알림 (Promise 이행)
				resolve.Invoke()
			}()

			return nil
		})

		return _Promise.New(promiseFn)
	})

	// cancel: 스트림이 JS 쪽에서 취소될 때 호출됨
	onCancel = js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		// Go 리더기(ReadCloser)를 닫아 리소스를 정리합니다.
		rs.closeOnce.Do(func() {
			rs.r.Close()
		})
		return nil
	})

	// 8. JS 'underlyingSource' 객체 생성
	underlyingSource := _Object.New()
	underlyingSource.Set("start", onStart)
	underlyingSource.Set("pull", onPull)
	underlyingSource.Set("cancel", onCancel)
	// underlyingSource.Set("type", "bytes") // Safari does not support ReadableByteStreamController

	// 9. JS ReadableStream 인스턴스 생성
	stream := _ReadableStream.New(underlyingSource)

	// 10. Go 래퍼 구조체 필드 완성
	rs.Value = stream
	rs.funcsToBeReleased = []js.Func{onStart, onPull, onCancel}

	return rs
}

// Close는 스트림을 닫고 할당된 JS 함수들을 해제(release)합니다.
func (rs *ReadableStream) Close() {
	for _, f := range rs.funcsToBeReleased {
		f.Release()
	}

	// Go 리더기도 닫아줍니다.
	rs.closeOnce.Do(func() {
		rs.r.Close()
	})
}
