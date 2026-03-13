package utils

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gosuda/portal/v2/types"
)

func TestWriteAPIDataAndDecodeEnvelope(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	WriteAPIData(rec, http.StatusCreated, map[string]string{"status": "ok"})

	if rec.Code != http.StatusCreated {
		t.Fatalf("WriteAPIData() status = %d, want %d", rec.Code, http.StatusCreated)
	}

	envelope, err := DecodeAPIEnvelope[map[string]string](bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("DecodeAPIEnvelope() error = %v", err)
	}
	if !envelope.OK || envelope.Data["status"] != "ok" {
		t.Fatalf("DecodeAPIEnvelope() = %+v, want ok envelope", envelope)
	}
}

func TestDecodeAPIRequestError(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader(`{"ok":false,"error":{"code":"unauthorized","message":"denied"}}`)),
	}

	err := DecodeAPIRequestError(resp)
	var apiErr *types.APIRequestError
	if !errors.As(err, &apiErr) {
		t.Fatalf("DecodeAPIRequestError() error = %T, want *types.APIRequestError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden || apiErr.Code != "unauthorized" || apiErr.Message != "denied" {
		t.Fatalf("DecodeAPIRequestError() = %+v, want status/code/message populated", apiErr)
	}
}
