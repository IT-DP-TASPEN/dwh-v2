package reports

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/reporting"
)

const maxOrganizationFormBody = 8 << 10

type FolderOption struct {
	ID       uint64
	Name     string
	Selected bool
}

type ReportCard struct {
	Value             reporting.RuntimeReport
	FolderOptions     []FolderOption
	CurrentFolderName string
	Unfiled           bool
	ReturnQuery       string
}

type FolderView struct {
	Value         reporting.UserReportFolder
	Current       bool
	Editing       bool
	URL           string
	RenameValue   string
	NameError     string
	DeleteMessage string
}

type OrganizationData struct {
	Query, Heading, EmptyMessage, ReturnQuery, AllURL, StarredURL string
	StarredScope, FolderScope                                     bool
	CurrentFolderID                                               uint64
	Rows, StarredRows                                             []ReportCard
	Folders                                                       []FolderView
	StarredVisibleCount                                           int
	CreateFolderError                                             string
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	filter, err := organizationFilter(request)
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	data, err := handler.organizationData(request, filter, 0, "", "", "")
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "list reports", err)
		return
	}
	handler.renderOrganization(writer, request, http.StatusOK, data)
}

func (handler *Handler) Star(writer http.ResponseWriter, request *http.Request) {
	filter, ok := handler.organizationMutation(writer, request)
	if !ok {
		return
	}
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	var starred bool
	switch request.PostFormValue("starred") {
	case "true":
		starred = true
	case "false":
	default:
		http.Error(writer, "Invalid star state.", http.StatusUnprocessableEntity)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.reports.SetReportStarred(request.Context(), principal.UserID, id, starred, time.Now().UTC()); err != nil {
		handler.organizationMutationError(writer, request, "set report star", err)
		return
	}
	handler.organizationMutationSuccess(writer, request, filter)
}

func (handler *Handler) MoveToFolder(writer http.ResponseWriter, request *http.Request) {
	filter, ok := handler.organizationMutation(writer, request)
	if !ok {
		return
	}
	id, ok := idParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	var folderID *uint64
	if raw := request.PostFormValue("folder_id"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || parsed == 0 {
			http.Error(writer, "Invalid folder.", http.StatusUnprocessableEntity)
			return
		}
		folderID = &parsed
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.reports.MoveReportToFolder(request.Context(), principal.UserID, id, folderID, time.Now().UTC()); err != nil {
		handler.organizationMutationError(writer, request, "move report", err)
		return
	}
	handler.organizationMutationSuccess(writer, request, filter)
}

func (handler *Handler) CreateFolder(writer http.ResponseWriter, request *http.Request) {
	filter, ok := handler.organizationMutation(writer, request)
	if !ok {
		return
	}
	name := reporting.NormalizeFolderName(request.PostFormValue("name"))
	if err := reporting.ValidateFolderName(name); err != nil {
		handler.renderOrganizationError(writer, request, filter, 0, "", "", err.Error())
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	_, err := handler.reports.CreateUserReportFolder(request.Context(), principal.UserID, name, time.Now().UTC())
	if errors.Is(err, reporting.ErrFolderNameTaken) {
		handler.renderOrganizationError(writer, request, filter, 0, "", "", "Folder name already exists.")
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "create report folder", err)
		return
	}
	http.Redirect(writer, request, reportsURL(filter), http.StatusSeeOther)
}

func (handler *Handler) RenameFolder(writer http.ResponseWriter, request *http.Request) {
	filter, ok := handler.organizationMutation(writer, request)
	if !ok {
		return
	}
	folderID, ok := folderIDParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	submittedName := request.PostFormValue("name")
	name := reporting.NormalizeFolderName(submittedName)
	if err := reporting.ValidateFolderName(name); err != nil {
		handler.renderOrganizationError(writer, request, filter, folderID, submittedName, err.Error(), "")
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	err := handler.reports.RenameUserReportFolder(request.Context(), principal.UserID, folderID, name, time.Now().UTC())
	if errors.Is(err, reporting.ErrFolderNameTaken) {
		handler.renderOrganizationError(writer, request, filter, folderID, submittedName, "Folder name already exists.", "")
		return
	}
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "rename report folder", err)
		return
	}
	http.Redirect(writer, request, reportsURL(filter), http.StatusSeeOther)
}

func (handler *Handler) DeleteFolder(writer http.ResponseWriter, request *http.Request) {
	filter, ok := handler.organizationMutation(writer, request)
	if !ok {
		return
	}
	folderID, ok := folderIDParam(request)
	if !ok {
		handler.admin.NotFound(writer, request)
		return
	}
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	if err := handler.reports.DeleteUserReportFolder(request.Context(), principal.UserID, folderID); errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	} else if err != nil {
		handler.admin.Internal(writer, request, "delete report folder", err)
		return
	}
	if filter.FolderID != nil && *filter.FolderID == folderID {
		filter.FolderID = nil
	}
	http.Redirect(writer, request, reportsURL(filter), http.StatusSeeOther)
}

func (handler *Handler) organizationMutation(writer http.ResponseWriter, request *http.Request) (reporting.RuntimeReportFilter, bool) {
	filter, err := organizationFilter(request)
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return reporting.RuntimeReportFilter{}, false
	}
	if !webutil.ParseForm(writer, request, maxOrganizationFormBody) {
		return reporting.RuntimeReportFilter{}, false
	}
	return filter, true
}

func (handler *Handler) organizationMutationSuccess(writer http.ResponseWriter, request *http.Request, filter reporting.RuntimeReportFilter) {
	if request.Header.Get("HX-Request") != "true" {
		http.Redirect(writer, request, reportsURL(filter), http.StatusSeeOther)
		return
	}
	data, err := handler.organizationData(request, filter, 0, "", "", "")
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "refresh reports", err)
		return
	}
	handler.renderOrganizationPartial(writer, request, http.StatusOK, data)
}

func (handler *Handler) organizationMutationError(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	if errors.Is(err, reporting.ErrForbidden) {
		handler.admin.RenderPage(writer, request, http.StatusForbidden, "forbidden", "Forbidden", nil)
		return
	}
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	if errors.Is(err, reporting.ErrInvalid) {
		http.Error(writer, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
		return
	}
	handler.admin.Internal(writer, request, operation, err)
}

func (handler *Handler) renderOrganizationError(writer http.ResponseWriter, request *http.Request, filter reporting.RuntimeReportFilter, renameFolderID uint64, renameValue, renameError, createError string) {
	data, err := handler.organizationData(request, filter, renameFolderID, renameValue, renameError, createError)
	if errors.Is(err, reporting.ErrNotFound) {
		handler.admin.NotFound(writer, request)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "render report folder error", err)
		return
	}
	handler.renderOrganization(writer, request, http.StatusUnprocessableEntity, data)
}

func (handler *Handler) organizationData(request *http.Request, filter reporting.RuntimeReportFilter, renameFolderID uint64, renameValue, renameError, createError string) (OrganizationData, error) {
	principal, _ := browserauth.CurrentPrincipal(request.Context())
	organization, err := handler.reports.ListRuntimeReportOrganization(request.Context(), principal.UserID, filter)
	if err != nil {
		return OrganizationData{}, err
	}
	data := OrganizationData{
		Query: filter.Query, Heading: "All Reports", EmptyMessage: "No reports are currently available to you.",
		ReturnQuery: reportsQuery(filter), AllURL: reportsURL(reporting.RuntimeReportFilter{Query: filter.Query}),
		StarredURL: reportsURL(reporting.RuntimeReportFilter{Query: filter.Query, Starred: true}),
		Rows:       make([]ReportCard, 0, len(organization.Reports)), StarredRows: make([]ReportCard, 0),
		Folders: make([]FolderView, 0, len(organization.Folders)), StarredVisibleCount: organization.StarredVisibleCount,
		CreateFolderError: createError,
	}
	if filter.Starred {
		data.StarredScope, data.Heading, data.EmptyMessage = true, "Starred", "No starred reports yet."
	} else if filter.FolderID != nil {
		data.FolderScope, data.CurrentFolderID, data.EmptyMessage = true, *filter.FolderID, "No reports in this folder."
	} else if filter.Query != "" {
		data.EmptyMessage = "No reports match this search."
	}
	if filter.Starred && filter.Query != "" {
		data.EmptyMessage = "No starred reports match this search."
	}
	for _, folder := range organization.Folders {
		view := FolderView{Value: folder, Current: filter.FolderID != nil && folder.ID == *filter.FolderID, URL: reportsURL(reporting.RuntimeReportFilter{Query: filter.Query, FolderID: &folder.ID}), DeleteMessage: folderDeleteMessage(folder)}
		if folder.ID == renameFolderID {
			view.Editing = true
			view.RenameValue = renameValue
			view.NameError = renameError
		}
		if view.Current {
			data.Heading = folder.Name
		}
		data.Folders = append(data.Folders, view)
	}
	for _, report := range organization.Reports {
		card := ReportCard{Value: report, Unfiled: report.FolderID == nil, FolderOptions: make([]FolderOption, 0, len(organization.Folders)), ReturnQuery: data.ReturnQuery}
		for _, folder := range organization.Folders {
			selected := report.FolderID != nil && *report.FolderID == folder.ID
			card.FolderOptions = append(card.FolderOptions, FolderOption{ID: folder.ID, Name: folder.Name, Selected: selected})
			if selected {
				card.CurrentFolderName = folder.Name
			}
		}
		data.Rows = append(data.Rows, card)
		if !filter.Starred && filter.FolderID == nil && report.Starred {
			data.StarredRows = append(data.StarredRows, card)
		}
	}
	return data, nil
}

func (handler *Handler) renderOrganization(writer http.ResponseWriter, request *http.Request, status int, data OrganizationData) {
	pageData, ok := handler.admin.PageData(request, "Reports", data)
	if !ok {
		handler.admin.Internal(writer, request, "prepare reports page", errors.New("principal missing"))
		return
	}
	name := "admin"
	if request.Header.Get("HX-Request") == "true" {
		name = "report-browser"
	}
	if err := handler.admin.RenderPartial(writer, status, "features/reports/index", name, pageData); err != nil {
		handler.admin.Internal(writer, request, "render reports", err)
	}
}

func (handler *Handler) renderOrganizationPartial(writer http.ResponseWriter, request *http.Request, status int, data OrganizationData) {
	pageData, ok := handler.admin.PageData(request, "Reports", data)
	if !ok {
		handler.admin.Internal(writer, request, "prepare reports partial", errors.New("principal missing"))
		return
	}
	if err := handler.admin.RenderPartial(writer, status, "features/reports/index", "report-browser", pageData); err != nil {
		handler.admin.Internal(writer, request, "render reports partial", err)
	}
}

func organizationFilter(request *http.Request) (reporting.RuntimeReportFilter, error) {
	query := request.URL.Query()
	filter := reporting.RuntimeReportFilter{Query: strings.TrimSpace(query.Get("q"))}
	if values, found := query["starred"]; found {
		if len(values) != 1 || values[0] != "1" {
			return reporting.RuntimeReportFilter{}, reporting.ErrInvalid
		}
		filter.Starred = true
	}
	if values, found := query["folder"]; found {
		if len(values) != 1 {
			return reporting.RuntimeReportFilter{}, reporting.ErrInvalid
		}
		folderID, err := strconv.ParseUint(values[0], 10, 64)
		if err != nil || folderID == 0 || filter.Starred {
			return reporting.RuntimeReportFilter{}, reporting.ErrInvalid
		}
		filter.FolderID = &folderID
	}
	return filter, nil
}

func reportsURL(filter reporting.RuntimeReportFilter) string {
	return "/reports" + reportsQuery(filter)
}

func reportsQuery(filter reporting.RuntimeReportFilter) string {
	values := url.Values{}
	if filter.Query != "" {
		values.Set("q", filter.Query)
	}
	if filter.Starred {
		values.Set("starred", "1")
	} else if filter.FolderID != nil {
		values.Set("folder", strconv.FormatUint(*filter.FolderID, 10))
	}
	if encoded := values.Encode(); encoded != "" {
		return "?" + encoded
	}
	return ""
}

func folderIDParam(request *http.Request) (uint64, bool) {
	value, err := strconv.ParseUint(chi.URLParam(request, "folderID"), 10, 64)
	return value, err == nil && value != 0
}

func folderDeleteMessage(folder reporting.UserReportFolder) string {
	return fmt.Sprintf("Reports will not be deleted. %d currently visible reports will return to No Folder / All Reports.", folder.VisibleReportCount)
}
