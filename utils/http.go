package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func HTTPDo(ctx context.Context, client *http.Client, method, rawURL string, body io.Reader, headers http.Header) (*http.Response, error) {
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

	resp, err := HTTPDo(ctx, client, method, rawURL, body, reqHeaders)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func HTTPDoAPI(ctx context.Context, client *http.Client, method, rawURL string, payload any, headers http.Header, out any) error {
	body, reqHeaders, err := httpJSONRequest(payload, headers)
	if err != nil {
		return err
	}

	resp, err := HTTPDo(ctx, client, method, rawURL, body, reqHeaders)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return DecodeAPIRequestError(resp)
	}

	envelope, err := DecodeAPIEnvelope[json.RawMessage](resp.Body)
	if err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !envelope.OK {
		return NewAPIRequestError(resp.StatusCode, envelope.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

func HTTPReadString(resp *http.Response, limit int64) (string, error) {
	if resp == nil {
		return "", nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", err
	}
	return string(body), nil
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
