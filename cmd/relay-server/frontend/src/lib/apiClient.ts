export type APIErrorPayload = {
  code?: string;
  message?: string;
};

export type APIEnvelope<T> = {
  ok?: boolean;
  data?: T;
  error?: APIErrorPayload;
};

export class APIClientError extends Error {
  readonly code: string;
  readonly details: unknown;
  readonly status: number;

  constructor(message: string, status: number, code = "request_failed", details?: unknown) {
    super(message);
    this.name = "APIClientError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function headersToObject(headers?: HeadersInit): Record<string, string> {
  if (!headers) {
    return {};
  }
  if (headers instanceof Headers) {
    return Object.fromEntries(headers.entries());
  }
  if (Array.isArray(headers)) {
    return Object.fromEntries(headers);
  }
  return { ...headers };
}

function ensureJsonEnvelope<T>(raw: unknown, path: string): APIEnvelope<T> {
  if (!isRecord(raw)) {
    throw new APIClientError(
      `Unexpected API response for ${path}: envelope is not an object`,
      0,
      "invalid_envelope",
      raw
    );
  }

  const okValue = raw.ok;
  if (typeof okValue !== "boolean") {
    throw new APIClientError(
      `Unexpected API response for ${path}: missing ok flag`,
      0,
      "invalid_envelope",
      raw
    );
  }

  const errorValue = raw.error;
  if (errorValue !== undefined && !isRecord(errorValue)) {
    throw new APIClientError(
      `Unexpected API response for ${path}: invalid error payload`,
      0,
      "invalid_envelope",
      errorValue
    );
  }

  return {
    ok: okValue,
    data: (raw as { data?: T }).data,
    error: errorValue
      ? {
          code: typeof errorValue.code === "string" ? errorValue.code : "request_failed",
          message:
            typeof errorValue.message === "string"
              ? errorValue.message
              : "Request failed",
        }
      : undefined,
  };
}

function coerceErrorPayload(value: unknown): APIErrorPayload | null {
  if (!isRecord(value)) {
    return null;
  }

  const code = typeof value.code === "string" ? value.code.trim() : "";
  const message = typeof value.message === "string" ? value.message.trim() : "";

  if (!code && !message) {
    return null;
  }

  return {
    code: code || undefined,
    message: message || undefined,
  };
}

function extractErrorPayload(raw: unknown): APIErrorPayload | null {
  if (!isRecord(raw)) {
    return null;
  }

  const nested = coerceErrorPayload(raw.error);
  if (nested) {
    return nested;
  }

  return coerceErrorPayload(raw);
}

async function decodeEnvelope<T>(path: string, response: Response): Promise<APIEnvelope<T>> {
  const text = await response.text();
  if (!text) {
    if (response.ok) {
      return { ok: true };
    }
    throw new APIClientError(
      `Empty API response from ${path}`,
      response.status,
      "empty_response"
    );
  }

  let payload: unknown;
  try {
    payload = JSON.parse(text);
  } catch {
    throw new APIClientError(
      `API response from ${path} was not valid JSON`,
      response.status,
      "invalid_json",
      text
    );
  }

  if (isRecord(payload) && typeof payload.ok === "boolean") {
    return ensureJsonEnvelope<T>(payload, path);
  }

  if (response.ok) {
    return {
      ok: true,
      data: payload as T,
    };
  }

  const fallbackError = extractErrorPayload(payload);
  if (fallbackError) {
    return {
      ok: false,
      data: payload as T,
      error: {
        code: fallbackError.code || "request_failed",
        message: fallbackError.message || response.statusText || "Request failed",
      },
    };
  }

  throw new APIClientError(
    `Unexpected API response for ${path}: missing ok envelope`,
    response.status,
    "invalid_envelope",
    payload
  );
}

async function request<T>(path: string, init: RequestInit): Promise<T> {
  let response: Response;
  try {
    const requestHeaders = headersToObject(init.headers);
    response = await fetch(path, {
      credentials: "same-origin",
      ...init,
      headers: {
        Accept: "application/json",
        ...requestHeaders,
      },
    });
  } catch (error) {
    const isAbortError =
      error instanceof DOMException && error.name === "AbortError";
    throw new APIClientError(
      isAbortError ? "Request was aborted" : "Network request failed",
      0,
      isAbortError ? "aborted" : "network_error",
      error
    );
  }

  const envelope = await decodeEnvelope<T>(path, response);
  if (envelope.ok) {
    return envelope.data as T;
  }

  const message =
    envelope.error?.message?.trim() || response.statusText || "Request failed";
  const code = envelope.error?.code?.trim() || "request_failed";
  throw new APIClientError(message, response.status, code, envelope.data);
}

function jsonRequestInit(method: "POST" | "DELETE", body?: unknown): RequestInit {
  if (body === undefined) {
    return { method, headers: {} };
  }

  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  };
}

export const apiClient = {
  get<T>(path: string): Promise<T> {
    return request<T>(path, { method: "GET" });
  },
  post<T>(path: string, body?: unknown): Promise<T> {
    return request<T>(path, jsonRequestInit("POST", body));
  },
  delete<T>(path: string): Promise<T> {
    return request<T>(path, jsonRequestInit("DELETE"));
  },
};
