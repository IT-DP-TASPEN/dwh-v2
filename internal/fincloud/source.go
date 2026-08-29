package fincloud

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
)

type Location struct {
	ID          string `json:"id"`
	Description string `json:"descr"`
}

type AccountCode struct {
	ID          string `json:"id"`
	Description string `json:"descr"`
}

type JournalTransactionType struct {
	ID          string `json:"id"`
	Description string `json:"descr"`
}

func (c *Client) FetchAccessibleLocations(ctx context.Context) ([]Location, error) {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result struct {
				Locations []Location `json:"locationid"`
			} `json:"result"`
		} `json:"data"`
	}
	diagnostic, err := c.getJSON(ctx, "fetch accessible locations", "/admin/access/listvalues", &payload)
	if err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, applicationFailure("fetch accessible locations", "Fincloud reported a source failure", diagnostic, payload.Status, "")
	}
	return payload.Data.Result.Locations, nil
}

func (c *Client) FetchAccountCodes(ctx context.Context) ([]AccountCode, error) {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result struct {
				AccountCodes []AccountCode `json:"noakun"`
			} `json:"result"`
		} `json:"data"`
	}
	diagnostic, err := c.getJSON(ctx, "fetch account codes", "/bukuBesar/laporan/mutasiAkun//listvalues", &payload)
	if err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, applicationFailure("fetch account codes", "Fincloud reported a source failure", diagnostic, payload.Status, "")
	}
	return payload.Data.Result.AccountCodes, nil
}

func (c *Client) FetchJournalTransactionTypes(ctx context.Context) ([]JournalTransactionType, error) {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result struct {
				TransactionTypes *[]JournalTransactionType `json:"jenistransaksi"`
			} `json:"result"`
		} `json:"data"`
	}
	diagnostic, err := c.getJSON(ctx, "fetch journal transaction types", "/bukuBesar/laporan/jurnal//listvalues", &payload)
	if err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, applicationFailure("fetch journal transaction types", "Fincloud reported a source failure", diagnostic, payload.Status, "")
	}
	if payload.Data.Result.TransactionTypes == nil {
		diagnostic.FailureKind, diagnostic.DecodeStage = "missing_required", "journal_transaction_types"
		return nil, &Error{Kind: ErrorMalformed, Operation: "fetch journal transaction types", Message: "Fincloud journal transaction-type listing omitted required result array", diagnostic: diagnostic}
	}
	return *payload.Data.Result.TransactionTypes, nil
}

func (c *Client) FetchCIFNumbers(ctx context.Context, throughDate string) ([]string, error) {
	content, err := c.DownloadReport(ctx, "CIF Opening Report", "", "1900-01-01", throughDate)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(content, "\uFEFF")))
	reader.Comma = '|'
	headers, err := reader.Read()
	if err != nil {
		return nil, &Error{Kind: ErrorMalformed, Operation: "fetch CIF numbers", Message: "Fincloud CIF listing was malformed", Cause: err}
	}
	column := -1
	for index, header := range headers {
		if header == "CIF No" {
			column = index
			break
		}
	}
	if column < 0 {
		return nil, &Error{Kind: ErrorMalformed, Operation: "fetch CIF numbers", Message: "Fincloud CIF listing omitted CIF No"}
	}
	var values []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || column >= len(row) {
			return nil, &Error{Kind: ErrorMalformed, Operation: "fetch CIF numbers", Message: "Fincloud CIF listing was malformed", Cause: err}
		}
		var cif string
		cif, err = decodeCIFReportValue(row[column])
		if err != nil {
			return nil, &Error{Kind: ErrorMalformed, Operation: "fetch CIF numbers", Message: "Invalid CIF number format", Cause: err}
		}
		values = append(values, cif)
	}
	return normalizeIdentifiers(values), nil
}

func decodeCIFReportValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}

	// CIF Opening Report has an extra quoted layer:
	//
	// Raw CSV:
	//   """00100000001"""
	//
	// csv.Reader produces:
	//   "00100000001"
	//
	// Decode the remaining quoted representation.
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		var decoded string
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return "", err
		}
		return strings.TrimSpace(decoded), nil
	}

	return value, nil
}

func (c *Client) FetchSavingAccounts(ctx context.Context) ([]string, error) {
	return c.fetchAccountList(ctx, "fetch saving accounts", "/tabungan/inquiry/rekening/cari", url.Values{
		"cabang": {"ALL"}, "datamutasi": {"false"}, "pagenumber": {"0"}, "pagesize": {"9999999999999"}, "rowcount": {"0"},
	}, false)
}

func (c *Client) FetchTimeDepositAccounts(ctx context.Context) ([]string, error) {
	return c.fetchAccountList(ctx, "fetch time-deposit accounts", "/deposito/inquiry/rekening/cari", url.Values{
		"cabang": {"ALL"}, "pagenumber": {"0"}, "pagesize": {"9999999999999"}, "rowcount": {"0"}, "status": {""},
	}, false)
}

func (c *Client) FetchLoanAccounts(ctx context.Context) ([]string, error) {
	var all []string
	for _, status := range []string{"Aktif", "Closed", "WO", "HT"} {
		values, err := c.fetchAccountList(ctx, "fetch loan accounts", "/pinjaman/inquiry/rekening/cari", url.Values{
			"cabang": {"ALL"}, "jenispinjaman": {""}, "pagenumber": {"0"}, "pagesize": {"9999999999999"}, "rowcount": {"0"}, "status": {status},
		}, true)
		if err != nil {
			return nil, fmt.Errorf("loan status %s: %w", status, err)
		}
		all = append(all, values...)
	}
	return normalizeIdentifiers(all), nil
}

func (c *Client) fetchAccountList(ctx context.Context, operation, endpoint string, query url.Values, nullResultIsEmpty bool) ([]string, error) {
	var payload struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	diagnostic, err := c.getJSON(ctx, operation, endpoint+"?"+query.Encode(), &payload)
	if err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, applicationFailure(operation, "Fincloud reported a source failure", diagnostic, payload.Status, "")
	}
	data := bytes.TrimSpace(payload.Data)
	if len(data) == 0 {
		diagnostic.FailureKind, diagnostic.DecodeStage = "missing_required", "account_list_data"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud account listing omitted required data object", diagnostic: diagnostic}
	}
	if bytes.Equal(data, []byte("null")) {
		diagnostic.FailureKind, diagnostic.DecodeStage = "dto_decode", "account_list_data"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud account listing data was malformed", Cause: fmt.Errorf("data must be a JSON object"), diagnostic: diagnostic}
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		diagnostic.FailureKind, diagnostic.DecodeStage = decodeFailureKind(err), "account_list_data"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud account listing data was malformed", Cause: err, diagnostic: diagnostic}
	}
	result := bytes.TrimSpace(envelope.Result)
	if len(result) == 0 {
		diagnostic.FailureKind, diagnostic.DecodeStage = "missing_required", "account_list_result"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud account listing omitted required result array", diagnostic: diagnostic}
	}
	if bytes.Equal(result, []byte("null")) {
		if nullResultIsEmpty {
			return []string{}, nil
		}
		diagnostic.FailureKind, diagnostic.DecodeStage = "missing_required", "account_list_result"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud account listing omitted required result array", diagnostic: diagnostic}
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result, &rows); err != nil {
		diagnostic.FailureKind, diagnostic.DecodeStage = decodeFailureKind(err), "account_list_result"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud account listing result was malformed", Cause: err, diagnostic: diagnostic}
	}
	values := make([]string, len(rows))
	for index, row := range rows {
		values[index] = row.ID
	}
	return normalizeIdentifiers(values), nil
}

func normalizeIdentifiers(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (c *Client) DownloadReport(ctx context.Context, name string, parameters ...string) (string, error) {
	encodedParameters, err := json.Marshal(parameters)
	if err != nil {
		return "", err
	}
	query := url.Values{"nm": {name}, "type": {"csv"}, "p": {string(encodedParameters)}}
	reportPath := "/system/laporanUmum/data/lap?" + strings.ReplaceAll(query.Encode(), "+", "%20")
	resp, err := c.do(ctx, "download report", func(sessionID string) (*http.Request, error) {
		form := url.Values{"sessionId": {sessionID}}
		req, err := c.newRequest(ctx, http.MethodGet, reportPath, strings.NewReader(form.Encode()), sessionID)
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req, err
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", c.upstreamResponseFailure("download report", resp)
	}
	data, body, err := c.readResponseBody(resp, true, "download report")
	if err != nil {
		diagnostic := c.responseDiagnostic(resp, body)
		diagnostic.FailureKind = "body_read"
		return "", &Error{Kind: ErrorUpstream, Operation: "download report", Message: "could not read Fincloud response", Cause: err, diagnostic: diagnostic}
	}
	return string(bytes.TrimPrefix(data, []byte("\uFEFF"))), nil
}

func (c *Client) DownloadMaintenanceReport(ctx context.Context, file, directory string) (string, error) {
	resp, err := c.do(ctx, "download maintenance report", func(sessionID string) (*http.Request, error) {
		req, err := c.newRequest(ctx, http.MethodGet, "/system/downloaderlaporan/download.php", nil, sessionID)
		if err != nil {
			return nil, err
		}
		query := req.URL.Query()
		query.Set("file", file)
		query.Set("path", directory)
		query.Set("sessionId", sessionID)
		req.URL.RawQuery = query.Encode()
		return req, nil
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", c.upstreamResponseFailure("download maintenance report", resp)
	}
	data, body, err := c.readResponseBody(resp, true, "download maintenance report")
	if err != nil {
		diagnostic := c.responseDiagnostic(resp, body)
		diagnostic.FailureKind = "body_read"
		return "", &Error{Kind: ErrorUpstream, Operation: "download maintenance report", Message: "could not read Fincloud response", Cause: err, diagnostic: diagnostic}
	}
	return string(bytes.TrimPrefix(data, []byte("\uFEFF"))), nil
}

func (c *Client) ListMaintenanceReportFiles(ctx context.Context, folder string) ([]string, error) {
	root := path.Join("/app/report", folder)
	return c.listMaintenanceDirectory(ctx, folder, "/app/report", root)
}

func (c *Client) listMaintenanceDirectory(ctx context.Context, file, directory, root string) ([]string, error) {
	if !pathWithin(path.Join(directory, file), root) {
		return nil, fmt.Errorf("maintenance report directory is outside requested date")
	}
	query := url.Values{"file": {file}, "jenis": {"Folder"}, "pathfolder": {directory}}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result struct {
				Path string `json:"pathfolder"`
				List []struct {
					File string `json:"file"`
					Kind string `json:"jenis"`
				} `json:"list"`
			} `json:"result"`
		} `json:"data"`
	}
	diagnostic, err := c.getJSON(ctx, "list maintenance reports", "/system/downloaderlaporan/pembuatan/loadorDownload?"+query.Encode(), &payload)
	if err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, applicationFailure("list maintenance reports", "Fincloud reported a source failure", diagnostic, payload.Status, "")
	}
	if payload.Data.Result.Path == "" && len(payload.Data.Result.List) == 0 {
		return nil, nil
	}
	if !pathWithin(payload.Data.Result.Path, root) {
		return nil, fmt.Errorf("maintenance report directory is outside requested date")
	}
	var files []string
	for _, item := range payload.Data.Result.List {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch item.Kind {
		case "Folder":
			children, err := c.listMaintenanceDirectory(ctx, item.File, payload.Data.Result.Path, root)
			if err != nil {
				return nil, err
			}
			files = append(files, children...)
		case "File":
			file := path.Join(payload.Data.Result.Path, item.File)
			if !pathWithin(file, root) {
				return nil, fmt.Errorf("maintenance report file is outside requested date")
			}
			files = append(files, file)
		}
	}
	return files, nil
}

func pathWithin(value, root string) bool {
	value, root = path.Clean(value), path.Clean(root)
	return value == root || strings.HasPrefix(value, root+"/")
}

func (c *Client) getJSON(ctx context.Context, operation, requestPath string, target any) (*DiagnosticPayload, error) {
	resp, err := c.do(ctx, operation, func(sessionID string) (*http.Request, error) {
		return c.newRequest(ctx, http.MethodGet, requestPath, nil, sessionID)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.upstreamResponseFailure(operation, resp)
	}
	data, body, readErr := c.readResponseBody(resp, true, operation)
	diagnostic := c.responseDiagnostic(resp, body)
	diagnostic.Application = applicationFromBody(body)
	if readErr != nil {
		diagnostic.FailureKind = "body_read"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "could not read Fincloud response", Cause: readErr, diagnostic: diagnostic}
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(target); err != nil {
		diagnostic.FailureKind, diagnostic.DecodeStage = decodeFailureKind(err), "response_dto"
		return nil, &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud response was malformed", Cause: err, diagnostic: diagnostic}
	}
	return diagnostic, nil
}

func applicationFailure(operation, message string, diagnostic *DiagnosticPayload, status, applicationMessage string) error {
	if diagnostic == nil {
		diagnostic = &DiagnosticPayload{}
	}
	diagnostic.FailureKind = "application"
	if diagnostic.Application.Status == "" {
		diagnostic.Application.Status = status
	}
	if diagnostic.Application.Message == "" {
		diagnostic.Application.Message = applicationMessage
	}
	return &Error{Kind: ErrorUpstream, Operation: operation, Message: message, diagnostic: diagnostic}
}

func applicationFromBody(body BodyDiagnostic) ApplicationDiagnostic {
	if body.Encoding != "utf8" {
		return ApplicationDiagnostic{}
	}
	var envelope struct {
		Status  any    `json:"status"`
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(body.Body), &envelope) != nil {
		return ApplicationDiagnostic{}
	}
	status := ""
	switch value := envelope.Status.(type) {
	case string:
		status = value
	case float64:
		status = fmt.Sprint(value)
	}
	return ApplicationDiagnostic{Status: status, Message: envelope.Message}
}
