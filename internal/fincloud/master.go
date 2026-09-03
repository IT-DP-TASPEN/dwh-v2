package fincloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

func (c *Client) FetchReferenceMaster(ctx context.Context, requestPath string) (map[string]json.RawMessage, error) {
	switch requestPath {
	case "/cif/inquiry/cif//listvalues", "/tabungan/inquiry/rekening//listvalues", "/deposito/inquiry/rekening//listvalues", "/pinjaman/inquiry/rekening//listvalues":
	default:
		return nil, fmt.Errorf("unsupported reference Master path")
	}
	result, diagnostic, err := c.fetchMasterResult(ctx, "fetch reference master", requestPath)
	if err != nil {
		return nil, err
	}
	var categories map[string]json.RawMessage
	if len(result) == 0 || bytes.Equal(result, []byte("null")) || json.Unmarshal(result, &categories) != nil || categories == nil {
		return nil, malformedMaster("fetch reference master", "reference result must be an object", diagnostic, "master_result")
	}
	return categories, nil
}

func (c *Client) FetchMarketingMaster(ctx context.Context) ([]json.RawMessage, error) {
	result, diagnostic, err := c.fetchMasterResult(ctx, "fetch marketing master", "/system/marketing/pembuatan/cari?nama=")
	if err != nil {
		return nil, err
	}
	var entities []json.RawMessage
	if len(result) == 0 || bytes.Equal(result, []byte("null")) || json.Unmarshal(result, &entities) != nil || entities == nil {
		return nil, malformedMaster("fetch marketing master", "marketing result must be an array", diagnostic, "master_result")
	}
	return entities, nil
}

func (c *Client) fetchMasterResult(ctx context.Context, operation, requestPath string) (json.RawMessage, *DiagnosticPayload, error) {
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	diagnostic, err := c.getJSON(ctx, operation, requestPath, &envelope)
	if err != nil {
		return nil, diagnostic, err
	}
	if envelope.Status != "ok" {
		return nil, diagnostic, applicationFailure(operation, "Fincloud reported a source failure", diagnostic, envelope.Status, "")
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) || data[0] != '{' {
		return nil, diagnostic, malformedMaster(operation, "data must be a present object", diagnostic, "master_data")
	}
	var payload struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, diagnostic, malformedMaster(operation, "data was malformed", diagnostic, "master_data")
	}
	result := bytes.TrimSpace(payload.Result)
	if len(result) == 0 || bytes.Equal(result, []byte("null")) {
		return nil, diagnostic, malformedMaster(operation, "result must be present and non-null", diagnostic, "master_result")
	}
	return result, diagnostic, nil
}

func malformedMaster(operation, message string, diagnostic *DiagnosticPayload, stage string) error {
	if diagnostic == nil {
		diagnostic = &DiagnosticPayload{}
	}
	diagnostic.FailureKind, diagnostic.DecodeStage = "missing_required", stage
	return &Error{Kind: ErrorMalformed, Operation: operation, Message: message, Cause: fmt.Errorf("source contract violation"), diagnostic: diagnostic}
}
