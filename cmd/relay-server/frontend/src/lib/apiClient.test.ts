import { APIClientError, apiClient } from "@/lib/apiClient";
import { beforeEach, describe, expect, it, vi } from "vitest";

function jsonResponse(payload: unknown, init?: ResponseInit): Response {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" },
    ...init,
  });
}

describe("apiClient", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock as unknown as typeof fetch);
  });

  it("returns data when API envelope is ok", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ ok: true, data: { value: 42 } }),
    );

    const data = await apiClient.get<{ value: number }>("/api/test");

    expect(data).toEqual({ value: 42 });
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe("GET");
    expect(init.credentials).toBe("same-origin");
    expect(init.headers).toEqual({ Accept: "application/json" });
  });

  it("accepts successful non-envelope JSON payloads", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ direct: true }));

    const data = await apiClient.get<{ direct: boolean }>("/api/test");

    expect(data).toEqual({ direct: true });
  });

  it("throws APIClientError for server-side envelope failures", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        { ok: false, error: { code: "forbidden", message: "Denied" } },
        { status: 403, statusText: "Forbidden" },
      ),
    );

    await expect(apiClient.get("/api/test")).rejects.toMatchObject({
      name: "APIClientError",
      status: 403,
      code: "forbidden",
      message: "Denied",
    } satisfies Partial<APIClientError>);
  });

  it("throws invalid_envelope when a failed response has no envelope", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ message: "not wrapped" }, { status: 400 }),
    );

    await expect(apiClient.get("/api/test")).rejects.toMatchObject({
      name: "APIClientError",
      status: 400,
      code: "invalid_envelope",
    } satisfies Partial<APIClientError>);
  });

  it("throws invalid_json when response body is not parseable JSON", async () => {
    fetchMock.mockResolvedValueOnce(
      new Response("not-json", {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );

    await expect(apiClient.get("/api/test")).rejects.toMatchObject({
      name: "APIClientError",
      status: 200,
      code: "invalid_json",
    } satisfies Partial<APIClientError>);
  });

  it("maps fetch failures to network_error", async () => {
    fetchMock.mockRejectedValueOnce(new Error("network down"));

    await expect(apiClient.get("/api/test")).rejects.toMatchObject({
      name: "APIClientError",
      status: 0,
      code: "network_error",
      message: "Network request failed",
    } satisfies Partial<APIClientError>);
  });

  it("maps AbortError failures to aborted", async () => {
    fetchMock.mockRejectedValueOnce(new DOMException("Aborted", "AbortError"));

    await expect(apiClient.get("/api/test")).rejects.toMatchObject({
      name: "APIClientError",
      status: 0,
      code: "aborted",
      message: "Request was aborted",
    } satisfies Partial<APIClientError>);
  });

  it("sends JSON bodies for post and omits content-type for delete without body", async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true, data: {} }));
    await apiClient.post("/api/post", { id: 1 });

    const postInit = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(postInit.method).toBe("POST");
    expect(postInit.body).toBe(JSON.stringify({ id: 1 }));
    expect(postInit.headers).toEqual({
      Accept: "application/json",
      "Content-Type": "application/json",
    });

    fetchMock.mockResolvedValueOnce(jsonResponse({ ok: true, data: {} }));
    await apiClient.delete("/api/post");

    const deleteInit = fetchMock.mock.calls[1]?.[1] as RequestInit;
    expect(deleteInit.method).toBe("DELETE");
    expect(deleteInit.headers).toEqual({
      Accept: "application/json",
    });
  });
});
