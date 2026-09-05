package customdatasets

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/customdataset"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
)

const multipartAllowance = 1 << 20

type Handler struct {
	admin      *adminshell.Shell
	repository *customdataset.Repository
	storage    *customdataset.Storage
}

type ListData struct {
	Rows      []customdataset.Dataset
	CanManage bool
}
type DetailData struct {
	Dataset   customdataset.Dataset
	Columns   []customdataset.Column
	Imports   []customdataset.Import
	Sample    [][]customdataset.SampleCell
	CanManage bool
	SQLName   string
}
type UploadData struct {
	Dataset *customdataset.Dataset
	Mode    customdataset.ImportMode
	Error   string
}
type ConfigureData struct {
	Upload        customdataset.Upload
	Dataset       *customdataset.Dataset
	Preview       *customdataset.Preview
	FrozenColumns []customdataset.Column
	Delimiter     customdataset.Delimiter
	HeaderRecord  uint64
	Mode          customdataset.ImportMode
	Error         string
}

func NewHandler(admin *adminshell.Shell, repository *customdataset.Repository, storage *customdataset.Storage) *Handler {
	return &Handler{admin: admin, repository: repository, storage: storage}
}

func (handler *Handler) RegisterRoutes(router chi.Router) {
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/custom-datasets", handler.Index)
	router.With(handler.admin.RequirePermission(PermissionManage)).Get("/custom-datasets/new", handler.New)
	router.With(handler.admin.RequirePermission(PermissionManage)).Get("/custom-datasets/{id}/imports/new", handler.NewImport)
	router.With(handler.admin.RequirePermission(PermissionManage)).Post("/custom-datasets/uploads", handler.Upload)
	router.With(handler.admin.RequirePermission(PermissionManage)).Get("/custom-datasets/uploads/{uploadID}/configure", handler.Configure)
	router.With(handler.admin.RequirePermission(PermissionManage)).Post("/custom-datasets/imports", handler.Submit)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/custom-datasets/{id}", handler.Show)
	router.With(handler.admin.RequirePermission(PermissionView)).Get("/custom-datasets/{id}/status", handler.Status)
	router.With(handler.admin.RequirePermission(PermissionManage)).Post("/custom-datasets/{id}/metadata", handler.Metadata)
	router.With(handler.admin.RequirePermission(PermissionManage)).Post("/custom-datasets/{id}/archive", handler.Archive)
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	rows, err := handler.repository.List(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "list custom datasets", err)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/customdatasets/index", "Custom datasets", ListData{Rows: rows, CanManage: principal.Can(PermissionManage)})
}

func (handler *Handler) New(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/customdatasets/upload", "New custom dataset", UploadData{Mode: customdataset.ModeReplace})
}

func (handler *Handler) NewImport(writer http.ResponseWriter, request *http.Request) {
	dataset, ok := handler.findDataset(writer, request)
	if !ok {
		return
	}
	if dataset.Status == customdataset.DatasetArchived {
		handler.admin.NotFound(writer, request)
		return
	}
	mode := customdataset.ImportMode(request.URL.Query().Get("mode"))
	if mode != customdataset.ModeAppend || dataset.Status != customdataset.DatasetActive {
		mode = customdataset.ModeReplace
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/customdatasets/upload", "Import CSV", UploadData{Dataset: &dataset, Mode: mode})
}

func (handler *Handler) Upload(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, customdataset.MaxUploadBytes+multipartAllowance)
	reader, err := request.MultipartReader()
	if err != nil {
		http.Error(writer, "A multipart CSV upload is required.", http.StatusBadRequest)
		return
	}
	var stored *customdataset.StoredFile
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			if stored != nil {
				_ = handler.storage.Remove(stored.Key)
			}
			handler.uploadError(writer, request, "The upload could not be read.")
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
			continue
		}
		if stored != nil {
			_ = handler.storage.Remove(stored.Key)
			handler.uploadError(writer, request, "Upload exactly one CSV file.")
			return
		}
		value, saveErr := handler.storage.Save(request.Context(), part.FileName(), part)
		_ = part.Close()
		if saveErr != nil {
			handler.uploadError(writer, request, publicError(saveErr))
			return
		}
		stored = &value
	}
	if stored == nil {
		handler.uploadError(writer, request, "Choose a CSV file.")
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	upload, err := handler.repository.CreateUpload(request.Context(), principal.SecurityContext(), *stored, time.Now().UTC().Add(24*time.Hour), time.Now().UTC())
	if err != nil {
		// Reconciliation removes definite orphans; a commit error can mean the upload metadata was stored.
		handler.uploadError(writer, request, publicError(err))
		return
	}
	query := request.URL.Query()
	location := fmt.Sprintf("/custom-datasets/uploads/%d/configure", upload.ID)
	if datasetID := query.Get("dataset_id"); datasetID != "" {
		location += "?dataset_id=" + datasetID + "&mode=" + query.Get("mode")
	}
	http.Redirect(writer, request, location, http.StatusSeeOther)
}

func (handler *Handler) uploadError(writer http.ResponseWriter, request *http.Request, message string) {
	var dataset *customdataset.Dataset
	if id, _ := strconv.ParseUint(request.URL.Query().Get("dataset_id"), 10, 64); id != 0 {
		if value, err := handler.repository.Find(request.Context(), id); err == nil {
			dataset = &value
		}
	}
	mode := customdataset.ImportMode(request.URL.Query().Get("mode"))
	if mode != customdataset.ModeAppend {
		mode = customdataset.ModeReplace
	}
	handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/customdatasets/upload", "Upload CSV", UploadData{Dataset: dataset, Mode: mode, Error: message})
}

func (handler *Handler) Configure(writer http.ResponseWriter, request *http.Request) {
	uploadID, ok := uintParam(request, "uploadID")
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	upload, err := handler.repository.FindUpload(request.Context(), uploadID)
	if err != nil {
		handler.admin.NotFound(writer, request)
		return
	}
	data := ConfigureData{Upload: upload, HeaderRecord: 1, Mode: customdataset.ModeReplace}
	if header, parseErr := strconv.ParseUint(request.URL.Query().Get("header"), 10, 64); parseErr == nil && header > 0 {
		data.HeaderRecord = header
	}
	if mode := customdataset.ImportMode(request.URL.Query().Get("mode")); mode == customdataset.ModeAppend {
		data.Mode = mode
	}
	if id, _ := strconv.ParseUint(request.URL.Query().Get("dataset_id"), 10, 64); id != 0 {
		dataset, findErr := handler.repository.Find(request.Context(), id)
		if findErr != nil || dataset.Status == customdataset.DatasetArchived {
			handler.admin.NotFound(writer, request)
			return
		}
		data.Dataset = &dataset
		if dataset.Status == customdataset.DatasetProvisioning {
			data.Mode = customdataset.ModeReplace
		} else {
			data.FrozenColumns, err = handler.repository.Columns(request.Context(), dataset.ID)
			if err != nil {
				handler.admin.Internal(writer, request, "load frozen custom dataset schema", err)
				return
			}
		}
	}
	file, err := handler.storage.Open(upload.StorageKey)
	if err != nil {
		handler.admin.Internal(writer, request, "open custom dataset upload", err)
		return
	}
	delimiter := customdataset.Delimiter(request.URL.Query().Get("delimiter"))
	if delimiter == "" {
		delimiter, ok, err = customdataset.DetectDelimiter(file)
		_ = file.Close()
		if err != nil {
			data.Error = publicError(err)
		} else if !ok {
			data.Error = "Delimiter is ambiguous. Choose comma, semicolon, or tab."
		} else {
			data.Delimiter = delimiter
		}
	} else {
		_ = file.Close()
		if _, err := delimiter.Rune(); err != nil {
			data.Error = "Choose a valid delimiter."
		} else {
			data.Delimiter = delimiter
		}
	}
	if data.Delimiter != "" {
		file, err = handler.storage.Open(upload.StorageKey)
		if err != nil {
			handler.admin.Internal(writer, request, "open custom dataset upload", err)
			return
		}
		words, wordsErr := handler.repository.ReservedWords(request.Context())
		if wordsErr != nil {
			_ = file.Close()
			handler.admin.Internal(writer, request, "load SQL reserved words", wordsErr)
			return
		}
		preview, previewErr := customdataset.ParsePreview(request.Context(), file, data.Delimiter, data.HeaderRecord, func(value string) bool { _, found := words[value]; return found })
		_ = file.Close()
		if previewErr != nil {
			data.Error = publicError(previewErr)
		} else if len(data.FrozenColumns) != 0 && !headerMatches(data.FrozenColumns, preview.Header) {
			data.Error = "CSV header does not match the frozen schema."
		} else {
			data.Preview = &preview
		}
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/customdatasets/configure", "Configure custom dataset", data)
}

func (handler *Handler) Submit(writer http.ResponseWriter, request *http.Request) {
	if !webutil.ParseForm(writer, request, 256<<10) {
		return
	}
	uploadID, _ := strconv.ParseUint(request.PostFormValue("upload_id"), 10, 64)
	datasetID, _ := strconv.ParseUint(request.PostFormValue("dataset_id"), 10, 64)
	revision, _ := strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	header, _ := strconv.ParseUint(request.PostFormValue("header_record"), 10, 64)
	delimiter := customdataset.Delimiter(request.PostFormValue("delimiter"))
	mode := customdataset.ImportMode(request.PostFormValue("mode"))
	upload, err := handler.repository.FindUpload(request.Context(), uploadID)
	if err != nil {
		http.Error(writer, "Upload not found.", http.StatusUnprocessableEntity)
		return
	}
	file, err := handler.storage.Open(upload.StorageKey)
	if err != nil {
		handler.admin.Internal(writer, request, "open custom dataset upload", err)
		return
	}
	words, err := handler.repository.ReservedWords(request.Context())
	if err != nil {
		_ = file.Close()
		handler.admin.Internal(writer, request, "load SQL reserved words", err)
		return
	}
	preview, err := customdataset.ParsePreview(request.Context(), file, delimiter, header, func(value string) bool { _, found := words[value]; return found })
	_ = file.Close()
	if err != nil {
		http.Error(writer, publicError(err), http.StatusUnprocessableEntity)
		return
	}
	columns := make([]customdataset.Column, len(preview.Header))
	for index := range columns {
		kind := customdataset.LogicalType(request.PostFormValue(fmt.Sprintf("type_%d", index)))
		dateFormat := strings.TrimSpace(request.PostFormValue(fmt.Sprintf("date_format_%d", index)))
		var format *string
		if kind == customdataset.TypeDate || kind == customdataset.TypeDateTime {
			format = &dateFormat
		}
		columns[index] = customdataset.Column{Ordinal: uint16(index + 1), DisplayName: preview.Header[index], QueryName: preview.QueryNames[index], PhysicalName: fmt.Sprintf("c%03d", index+1), LogicalType: kind, DateFormat: format}
	}
	var dataset customdataset.Dataset
	if datasetID != 0 {
		dataset, err = handler.repository.Find(request.Context(), datasetID)
		if err != nil {
			handler.admin.NotFound(writer, request)
			return
		}
		if dataset.Status == customdataset.DatasetActive {
			columns, err = handler.repository.Columns(request.Context(), datasetID)
			if err != nil {
				handler.admin.Internal(writer, request, "load custom dataset schema", err)
				return
			}
			if len(columns) != len(preview.Header) {
				http.Error(writer, "CSV header does not match the frozen schema.", http.StatusUnprocessableEntity)
				return
			}
			for index := range columns {
				if columns[index].DisplayName != preview.Header[index] {
					http.Error(writer, "CSV header does not match the frozen schema.", http.StatusUnprocessableEntity)
					return
				}
			}
			mode = customdataset.ImportMode(request.PostFormValue("mode"))
		} else {
			mode = customdataset.ModeReplace
		}
	} else {
		mode = customdataset.ModeReplace
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	created, _, err := handler.repository.Submit(request.Context(), principal.SecurityContext(), customdataset.Submission{Name: request.PostFormValue("name"), Description: request.PostFormValue("description"), DatasetID: datasetID, DatasetRevision: revision, UploadID: uploadID, Delimiter: delimiter, HeaderRecordNumber: header, Mode: mode, Columns: columns}, time.Now().UTC())
	if err != nil {
		http.Error(writer, publicError(err), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/custom-datasets/%d?notice=custom-dataset-import-submitted", created.ID), http.StatusSeeOther)
}

func (handler *Handler) Show(writer http.ResponseWriter, request *http.Request) {
	data, ok := handler.detail(writer, request)
	if !ok {
		return
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/customdatasets/show", data.Dataset.Name, data)
}

func (handler *Handler) Status(writer http.ResponseWriter, request *http.Request) {
	data, ok := handler.detail(writer, request)
	if !ok {
		return
	}
	if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/customdatasets/show", "custom-dataset-status", data); err != nil {
		handler.admin.Internal(writer, request, "render custom dataset status", err)
	}
}

func (handler *Handler) Metadata(writer http.ResponseWriter, request *http.Request) {
	id, ok := webutil.RouteID(request)
	if !ok || !webutil.ParseForm(writer, request, 16<<10) {
		return
	}
	revision, _ := strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.repository.UpdateMetadata(request.Context(), principal.SecurityContext(), id, revision, request.PostFormValue("name"), request.PostFormValue("description"), time.Now().UTC()); err != nil {
		http.Error(writer, publicError(err), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/custom-datasets/%d", id), http.StatusSeeOther)
}

func (handler *Handler) Archive(writer http.ResponseWriter, request *http.Request) {
	id, ok := webutil.RouteID(request)
	if !ok || !webutil.ParseForm(writer, request, 8<<10) {
		return
	}
	revision, _ := strconv.ParseUint(request.PostFormValue("revision"), 10, 64)
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.repository.Archive(request.Context(), principal.SecurityContext(), id, revision, time.Now().UTC()); err != nil {
		http.Error(writer, publicError(err), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/custom-datasets/%d", id), http.StatusSeeOther)
}

func (handler *Handler) detail(writer http.ResponseWriter, request *http.Request) (DetailData, bool) {
	dataset, ok := handler.findDataset(writer, request)
	if !ok {
		return DetailData{}, false
	}
	columns, err := handler.repository.Columns(request.Context(), dataset.ID)
	if err != nil {
		handler.admin.Internal(writer, request, "load custom dataset columns", err)
		return DetailData{}, false
	}
	imports, err := handler.repository.Imports(request.Context(), dataset.ID)
	if err != nil {
		handler.admin.Internal(writer, request, "load custom dataset imports", err)
		return DetailData{}, false
	}
	var sample [][]customdataset.SampleCell
	if dataset.CurrentGenerationID != nil {
		sample, err = handler.repository.Sample(request.Context(), dataset, 25)
		if err != nil {
			handler.admin.Internal(writer, request, "sample custom dataset", err)
			return DetailData{}, false
		}
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	return DetailData{Dataset: dataset, Columns: columns, Imports: imports, Sample: sample, CanManage: principal.Can(PermissionManage), SQLName: dataset.ViewName()}, true
}

func (handler *Handler) findDataset(writer http.ResponseWriter, request *http.Request) (customdataset.Dataset, bool) {
	id, ok := webutil.RouteID(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return customdataset.Dataset{}, false
	}
	dataset, err := handler.repository.Find(request.Context(), id)
	if errors.Is(err, customdataset.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return customdataset.Dataset{}, false
	}
	if err != nil {
		handler.admin.Internal(writer, request, "find custom dataset", err)
		return customdataset.Dataset{}, false
	}
	return dataset, true
}

func uintParam(request *http.Request, name string) (uint64, bool) {
	value, err := strconv.ParseUint(chi.URLParam(request, name), 10, 64)
	return value, err == nil && value != 0
}

func headerMatches(columns []customdataset.Column, header []string) bool {
	if len(columns) != len(header) {
		return false
	}
	for index := range columns {
		if columns[index].DisplayName != header[index] {
			return false
		}
	}
	return true
}

func publicError(err error) string {
	switch {
	case errors.Is(err, customdataset.ErrConflict):
		return "This dataset changed or already has an active import. Reload and try again."
	case errors.Is(err, customdataset.ErrInvalid), errors.Is(err, customdataset.ErrInactive):
		return err.Error()
	default:
		return "The operation could not be completed."
	}
}
