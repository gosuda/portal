type APIErrorPayload = {
  code?: string;
  message?: string;
};

type APIEnvelope<T> = {
  ok: boolean;
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

async function decodeEnvelope<T>(response: Response): Promise<APIEnvelope<T>> {
  const text = await response.text();
  if (!text) {
    throw new APIClientError("Empty API response", response.status, "empty_response");
  }

  let payload: unknown;
  try {
    payload = JSON.parse(text);
  } catch {
    throw new APIClientError(
      "API returned non-JSON payload",
      response.status,
      "invalid_json",
      text
    );
  }

  if (
    typeof payload !== "object" ||
    payload === null ||
    !("ok" in payload) ||
    typeof (payload as { ok?: unknown }).ok !== "boolean"
  ) {
    throw new APIClientError(
      "API response did not match envelope format",
      response.status,
      "invalid_envelope",
      payload
    );
  }

  return payload as APIEnvelope<T>;
}

async function request<T>(path: string, init: RequestInit): Promise<T> {
  const response = await fetch(path, init);
  const envelope = await decodeEnvelope<T>(response);

  if (envelope.ok) {
    return envelope.data as T;
  }

  const message = envelope.error?.message?.trim() || "Request failed";
  const code = envelope.error?.code?.trim() || "request_failed";
  throw new APIClientError(message, response.status, code, envelope.data);
}

function jsonRequestInit(method: "POST" | "DELETE", body?: unknown): RequestInit {
  if (body === undefined) {
    return { method };
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
