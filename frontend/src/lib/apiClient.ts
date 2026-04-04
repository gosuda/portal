type APIErrorPayload = {
  code?: string;
  message?: string;
};

type APIEnvelope<T> = {
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

function ensureJsonEnvelope<T>(raw: unknown, path: string, status: number): APIEnvelope<T> {
  if (!isRecord(raw) || typeof raw.ok !== "boolean") {
    throw new APIClientError(`Invalid API response for ${path}`, status, "invalid_envelope", raw);
  }
  if (raw.error !== undefined && !isRecord(raw.error)) {
    throw new APIClientError(`Invalid error payload for ${path}`, status, "invalid_envelope", raw.error);
  }
  const errorValue = raw.error;
  return {
    ok: raw.ok,
    data: (raw as { data?: T }).data,
    error: errorValue
      ? {
          code: typeof errorValue.code === "string" ? errorValue.code : "request_failed",
          message: typeof errorValue.message === "string" ? errorValue.message : "Request failed",
        }
      : undefined,
  };
}

async function decodeEnvelope<T>(path: string, response: Response): Promise<APIEnvelope<T>> {
  const text = await response.text();
  if (!text.trim()) {
    throw new APIClientError(
      `Empty API response from ${path}`,
      response.status,
      "invalid_envelope",
      text
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

  return ensureJsonEnvelope<T>(payload, path, response.status);
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
