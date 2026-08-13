package fincloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
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

func (c *Client) FetchAccessibleLocations(ctx context.Context) ([]Location, error) {
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result struct {
				Locations []Location `json:"locationid"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "fetch accessible locations", "/admin/access/listvalues", &payload); err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, &Error{Kind: ErrorUpstream, Operation: "fetch accessible locations", Message: "Fincloud reported a source failure"}
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
	if err := c.getJSON(ctx, "fetch account codes", "/bukuBesar/laporan/mutasiAkun//listvalues", &payload); err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, &Error{Kind: ErrorUpstream, Operation: "fetch account codes", Message: "Fincloud reported a source failure"}
	}
	return payload.Data.Result.AccountCodes, nil
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
		return "", responseError("download report", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &Error{Kind: ErrorUpstream, Operation: "download report", Message: "could not read Fincloud response", Cause: err}
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
		return "", responseError("download maintenance report", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &Error{Kind: ErrorUpstream, Operation: "download maintenance report", Message: "could not read Fincloud response", Cause: err}
	}
	return string(bytes.TrimPrefix(data, []byte("\uFEFF"))), nil
}

func (c *Client) ListMaintenanceReportFiles(ctx context.Context, folder string) ([]string, error) {
	return c.listMaintenanceDirectory(ctx, folder, "/app/report")
}

func (c *Client) listMaintenanceDirectory(ctx context.Context, file, directory string) ([]string, error) {
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
	if err := c.getJSON(ctx, "list maintenance reports", "/system/downloaderlaporan/pembuatan/loadorDownload?"+query.Encode(), &payload); err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, &Error{Kind: ErrorUpstream, Operation: "list maintenance reports", Message: "Fincloud reported a source failure"}
	}
	var files []string
	for _, item := range payload.Data.Result.List {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch item.Kind {
		case "Folder":
			children, err := c.listMaintenanceDirectory(ctx, item.File, payload.Data.Result.Path)
			if err != nil {
				return nil, err
			}
			files = append(files, children...)
		case "File":
			files = append(files, path.Join(payload.Data.Result.Path, item.File))
		}
	}
	return files, nil
}

func (c *Client) getJSON(ctx context.Context, operation, requestPath string, target any) error {
	resp, err := c.do(ctx, operation, func(sessionID string) (*http.Request, error) {
		return c.newRequest(ctx, http.MethodGet, requestPath, nil, sessionID)
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(operation, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return &Error{Kind: ErrorMalformed, Operation: operation, Message: "Fincloud response was malformed", Cause: err}
	}
	return nil
}
