/* tslint:disable */
/* eslint-disable */
/**
 * Initialize WASM module
 */
export function init(): void;
/**
 * Data interpreter for converting relay protocol to browser-friendly format
 */
export class DataInterpreter {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  /**
   * Parse relay protocol packet to browser message
   */
  static parsePacket(data: Uint8Array): any;
  /**
   * Create relay protocol packet from browser message
   */
  static createPacket(msg: any): Uint8Array;
}
/**
 * HTTP Adapter for file and API transfers
 */
export class HttpAdapter {
  free(): void;
  [Symbol.dispose](): void;
  constructor(base_url: string);
  /**
   * Send GET request
   */
  get(path: string): Promise<any>;
  /**
   * Send POST request with JSON body
   */
  postJson(path: string, body: any): Promise<any>;
  /**
   * Upload file
   */
  uploadFile(path: string, file_name: string, file_data: Uint8Array): Promise<any>;
  /**
   * Download file
   */
  downloadFile(path: string): Promise<Uint8Array>;
}
/**
 * Main proxy engine that handles all intercepted requests
 */
export class ProxyEngine {
  free(): void;
  [Symbol.dispose](): void;
  /**
   * Create a new proxy engine
   */
  constructor(server_url: string);
  /**
   * Check if a URL should be intercepted
   */
  shouldIntercept(url: string): boolean;
  /**
   * Handle HTTP request
   */
  handleHttpRequest(method: string, url: string, headers: any, body?: Uint8Array | null): Promise<any>;
  /**
   * Open WebSocket connection through tunnel
   */
  openWebSocket(url: string, protocols: string[]): Promise<any>;
  /**
   * Send WebSocket message
   */
  sendWebSocketMessage(tunnel_id: string, data: any, is_binary: boolean): Promise<void>;
  /**
   * Receive WebSocket message
   */
  receiveWebSocketMessage(tunnel_id: string): Promise<any>;
  /**
   * Close WebSocket
   */
  closeWebSocket(tunnel_id: string, code: number, reason: string): Promise<void>;
  /**
   * Connect to TCP server
   */
  connectTcp(host: string, port: number): Promise<any>;
  /**
   * Get status information
   */
  getStatus(): any;
}
export class RelayClient {
  private constructor();
  free(): void;
  [Symbol.dispose](): void;
  /**
   * Connect to RelayDNS server
   */
  static connect(server_url: string): Promise<RelayClient>;
  /**
   * Get relay server information
   */
  getRelayInfo(): Promise<any>;
  /**
   * Register a lease
   */
  registerLease(name: string, alpns: string[]): Promise<void>;
  /**
   * Get client credential ID
   */
  getCredentialId(): string;
  /**
   * Request connection to another peer
   */
  requestConnection(lease_id: string, _alpn: string): Promise<any>;
}
/**
 * WebSocket Data Adapter for browser
 */
export class WebSocketAdapter {
  free(): void;
  [Symbol.dispose](): void;
  constructor(url: string);
  /**
   * Connect to WebSocket
   */
  connect(): Promise<void>;
  /**
   * Set message callback
   */
  onMessage(callback: Function): void;
  /**
   * Set error callback
   */
  onError(callback: Function): void;
  /**
   * Send text message
   */
  sendText(message: string): void;
  /**
   * Send binary message
   */
  sendBinary(data: Uint8Array): void;
  /**
   * Close connection
   */
  close(): void;
}

export type InitInput = RequestInfo | URL | Response | BufferSource | WebAssembly.Module;

export interface InitOutput {
  readonly memory: WebAssembly.Memory;
  readonly __wbg_httpadapter_free: (a: number, b: number) => void;
  readonly httpadapter_new: (a: number, b: number) => number;
  readonly httpadapter_get: (a: number, b: number, c: number) => any;
  readonly httpadapter_postJson: (a: number, b: number, c: number, d: any) => any;
  readonly httpadapter_uploadFile: (a: number, b: number, c: number, d: number, e: number, f: number, g: number) => any;
  readonly httpadapter_downloadFile: (a: number, b: number, c: number) => any;
  readonly __wbg_websocketadapter_free: (a: number, b: number) => void;
  readonly websocketadapter_new: (a: number, b: number) => number;
  readonly websocketadapter_connect: (a: number) => any;
  readonly websocketadapter_onMessage: (a: number, b: any) => void;
  readonly websocketadapter_onError: (a: number, b: any) => void;
  readonly websocketadapter_sendText: (a: number, b: number, c: number) => [number, number];
  readonly websocketadapter_sendBinary: (a: number, b: number, c: number) => [number, number];
  readonly websocketadapter_close: (a: number) => [number, number];
  readonly __wbg_datainterpreter_free: (a: number, b: number) => void;
  readonly datainterpreter_parsePacket: (a: number, b: number) => [number, number, number];
  readonly datainterpreter_createPacket: (a: any) => [number, number, number, number];
  readonly __wbg_proxyengine_free: (a: number, b: number) => void;
  readonly proxyengine_new: (a: number, b: number) => number;
  readonly proxyengine_shouldIntercept: (a: number, b: number, c: number) => number;
  readonly proxyengine_handleHttpRequest: (a: number, b: number, c: number, d: number, e: number, f: any, g: number, h: number) => any;
  readonly proxyengine_openWebSocket: (a: number, b: number, c: number, d: number, e: number) => any;
  readonly proxyengine_sendWebSocketMessage: (a: number, b: number, c: number, d: any, e: number) => any;
  readonly proxyengine_receiveWebSocketMessage: (a: number, b: number, c: number) => any;
  readonly proxyengine_closeWebSocket: (a: number, b: number, c: number, d: number, e: number, f: number) => any;
  readonly proxyengine_connectTcp: (a: number, b: number, c: number, d: number) => any;
  readonly proxyengine_getStatus: (a: number) => any;
  readonly __wbg_relayclient_free: (a: number, b: number) => void;
  readonly relayclient_connect: (a: number, b: number) => any;
  readonly relayclient_getRelayInfo: (a: number) => any;
  readonly relayclient_registerLease: (a: number, b: number, c: number, d: number, e: number) => any;
  readonly relayclient_getCredentialId: (a: number) => [number, number];
  readonly relayclient_requestConnection: (a: number, b: number, c: number, d: number, e: number) => any;
  readonly init: () => void;
  readonly wasm_bindgen__convert__closures_____invoke__h75a085a52d492ff4: (a: number, b: number, c: any) => void;
  readonly wasm_bindgen__closure__destroy__h2dcad6e62f01cec1: (a: number, b: number) => void;
  readonly wasm_bindgen__convert__closures_____invoke__hed2088bf6dd2c621: (a: number, b: number, c: any) => void;
  readonly wasm_bindgen__closure__destroy__h8eb17c158a55b496: (a: number, b: number) => void;
  readonly wasm_bindgen__convert__closures_____invoke__h404cda7fa36c69a6: (a: number, b: number, c: any, d: any) => void;
  readonly __wbindgen_malloc: (a: number, b: number) => number;
  readonly __wbindgen_realloc: (a: number, b: number, c: number, d: number) => number;
  readonly __wbindgen_exn_store: (a: number) => void;
  readonly __externref_table_alloc: () => number;
  readonly __wbindgen_externrefs: WebAssembly.Table;
  readonly __wbindgen_free: (a: number, b: number, c: number) => void;
  readonly __externref_table_dealloc: (a: number) => void;
  readonly __wbindgen_start: () => void;
}

export type SyncInitInput = BufferSource | WebAssembly.Module;
/**
* Instantiates the given `module`, which can either be bytes or
* a precompiled `WebAssembly.Module`.
*
* @param {{ module: SyncInitInput }} module - Passing `SyncInitInput` directly is deprecated.
*
* @returns {InitOutput}
*/
export function initSync(module: { module: SyncInitInput } | SyncInitInput): InitOutput;

/**
* If `module_or_path` is {RequestInfo} or {URL}, makes a request and
* for everything else, calls `WebAssembly.instantiate` directly.
*
* @param {{ module_or_path: InitInput | Promise<InitInput> }} module_or_path - Passing `InitInput` directly is deprecated.
*
* @returns {Promise<InitOutput>}
*/
export default function __wbg_init (module_or_path?: { module_or_path: InitInput | Promise<InitInput> } | InitInput | Promise<InitInput>): Promise<InitOutput>;
