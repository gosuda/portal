package utils

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gosuda/portal/v2/types"
)

type APIErrorResponse struct {
	Status  int
	Code    string
	Message string
}

func (resp APIErrorResponse) Write(w http.ResponseWriter) {
	WriteAPIError(w, resp.Status, resp.Code, resp.Message)
}

func WriteAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, types.APIEnvelope[any]{OK: true, Data: data})
}

func WriteAPIEmpty(w http.ResponseWriter, status int) {
	WriteAPIData(w, status, map[string]any{})
}

func WriteAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, types.APIEnvelope[any]{
		OK:    false,
		Error: &types.APIError{Code: code, Message: message},
	})
}

func MethodNotAllowedError() APIErrorResponse {
	return APIErrorResponse{
		Status:  http.StatusMethodNotAllowed,
		Code:    types.APIErrorCodeMethodNotAllowed,
		Message: "method not allowed",
	}
}

func InvalidRequestError(err error) APIErrorResponse {
	return APIErrorResponse{
		Status:  http.StatusBadRequest,
		Code:    types.APIErrorCodeInvalidRequest,
		Message: err.Error(),
	}
}

func InvalidRequestMessage(message string) APIErrorResponse {
	return APIErrorResponse{
		Status:  http.StatusBadRequest,
		Code:    types.APIErrorCodeInvalidRequest,
		Message: message,
	}
}

func RequireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	MethodNotAllowedError().Write(w)
	return false
}

func ResolveAPIURL(baseURL *url.URL, path string) *url.URL {
	ref := &url.URL{Path: path}
	if baseURL == nil {
		return ref
	}
	return baseURL.ResolveReference(ref)
}

func httpDo(ctx context.Context, client *http.Client, method, rawURL string, body io.Reader, headers http.Header) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return client.Do(req)
}

func HTTPDoJSON(ctx context.Context, client *http.Client, method, rawURL string, payload any, headers http.Header, out any) error {
	body, reqHeaders, err := httpJSONRequest(payload, headers)
	if err != nil {
		return err
	}

	resp, err := httpDo(ctx, client, method, rawURL, body, reqHeaders)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if out == nil {
		return nil
	}
	return json.UnmarshalRead(resp.Body, out)
}

func HTTPDoAPIPath(ctx context.Context, client *http.Client, baseURL *url.URL, method, path string, payload any, headers http.Header, out any) error {
	body, reqHeaders, err := httpJSONRequest(payload, headers)
	if err != nil {
		return err
	}

	resp, err := httpDo(ctx, client, method, ResolveAPIURL(baseURL, path).String(), body, reqHeaders)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return DecodeAPIRequestError(resp)
	}

	var envelope types.APIEnvelope[jsontext.Value]
	if err := json.UnmarshalRead(resp.Body, &envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return &types.APIRequestError{
				StatusCode: resp.StatusCode,
				Message:    fmt.Sprintf("api request failed with status %d", resp.StatusCode),
			}
		}
		return &types.APIRequestError{
			StatusCode: resp.StatusCode,
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
		}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

func DecodeAPIRequestError(resp *http.Response) error {
	if resp == nil {
		return &types.APIRequestError{Message: "empty api response"}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var envelope types.APIEnvelope[jsontext.Value]
	if err := json.Unmarshal(body, &envelope); err == nil && !envelope.OK {
		if envelope.Error == nil {
			return &types.APIRequestError{
				StatusCode: resp.StatusCode,
				Message:    fmt.Sprintf("api request failed with status %d", resp.StatusCode),
			}
		}
		return &types.APIRequestError{
			StatusCode: resp.StatusCode,
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
		}
	}

	return &types.APIRequestError{
		StatusCode: resp.StatusCode,
		Message:    strings.TrimSpace(string(body)),
	}
}

func DecodeJSONRequest[T any](w http.ResponseWriter, r *http.Request, maxBytes int64) (T, bool) {
	var dst T
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	defer r.Body.Close()
	if err := json.UnmarshalRead(r.Body, &dst); err != nil {
		WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
		return dst, false
	}
	return dst, true
}

func DecodeJSONRequestAs[T any](w http.ResponseWriter, r *http.Request, maxBytes int64, invalid APIErrorResponse) (T, bool) {
	var dst T
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	defer r.Body.Close()
	if err := json.UnmarshalRead(r.Body, &dst); err != nil {
		invalid.Write(w)
		return dst, false
	}
	return dst, true
}

func httpJSONRequest(payload any, headers http.Header) (io.Reader, http.Header, error) {
	reqHeaders := make(http.Header, len(headers))
	for key, values := range headers {
		reqHeaders[key] = append([]string(nil), values...)
	}

	if payload == nil {
		return nil, reqHeaders, nil
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal payload: %w", err)
	}
	if reqHeaders.Get("Content-Type") == "" {
		reqHeaders.Set("Content-Type", "application/json")
	}
	return bytes.NewReader(buf), reqHeaders, nil
}
