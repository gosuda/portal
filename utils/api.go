package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gosuda/portal/v2/types"
)

func WriteAPIEnvelope[T any](w http.ResponseWriter, status int, envelope types.APIEnvelope[T]) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}

func WriteAPIData(w http.ResponseWriter, status int, data any) {
	WriteAPIEnvelope(w, status, types.APIEnvelope[any]{OK: true, Data: data})
}

func WriteAPIError(w http.ResponseWriter, status int, code, message string) {
	WriteAPIEnvelope(w, status, types.APIEnvelope[any]{
		OK:    false,
		Error: &types.APIError{Code: code, Message: message},
	})
}

func DecodeAPIEnvelope[T any](r io.Reader) (types.APIEnvelope[T], error) {
	var envelope types.APIEnvelope[T]
	if err := json.NewDecoder(r).Decode(&envelope); err != nil {
		return types.APIEnvelope[T]{}, err
	}
	return envelope, nil
}

func NewAPIRequestError(statusCode int, apiErr *types.APIError) *types.APIRequestError {
	if apiErr == nil {
		return &types.APIRequestError{
			StatusCode: statusCode,
			Message:    fmt.Sprintf("api request failed with status %d", statusCode),
		}
	}
	return &types.APIRequestError{
		StatusCode: statusCode,
		Code:       apiErr.Code,
		Message:    apiErr.Message,
	}
}

func DecodeAPIRequestError(resp *http.Response) error {
	if resp == nil {
		return &types.APIRequestError{Message: "empty api response"}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var envelope types.APIEnvelope[json.RawMessage]
	if err := json.Unmarshal(body, &envelope); err == nil && !envelope.OK {
		return NewAPIRequestError(resp.StatusCode, envelope.Error)
	}

	return &types.APIRequestError{
		StatusCode: resp.StatusCode,
		Message:    strings.TrimSpace(string(body)),
	}
}

func DecodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}
