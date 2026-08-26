package schedules

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	domain "github.com/ibldzn/go-admin/internal/scheduler"
)

const maxFormBody = 32 << 10

// Handler uses the concrete service because it combines bounded read models with the Phase 5 domain service.
type Handler struct {
	admin   *adminshell.Shell
	service *Service
}

func NewHandler(admin *adminshell.Shell, service *Service) *Handler {
	return &Handler{admin: admin, service: service}
}

func (handler *Handler) Schedules(writer http.ResponseWriter, request *http.Request) {
	filter := Filter{request.URL.Query().Get("job"), request.URL.Query().Get("enabled"), request.URL.Query().Get("archived")}
	data, err := handler.service.List(request.Context(), filter, webutil.Page(request))
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid ") {
			http.Error(writer, http.StatusText(400), 400)
			return
		}
		handler.admin.Internal(writer, request, "list schedules", err)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	data.CanCreate = principal.Can(PermissionCreate)
	handler.admin.RenderPage(writer, request, 200, "features/schedules/index", "Schedules", data)
}

func (handler *Handler) New(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, 200, "features/schedules/new", "New schedule", handler.blankForm())
}

func (handler *Handler) BulkNew(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/schedules/bulk", "Bulk create schedules", handler.blankBulkForm())
}

func (handler *Handler) Create(writer http.ResponseWriter, request *http.Request) {
	form, ok := handler.form(writer, request)
	if !ok {
		return
	}
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, 422, "features/schedules/new", "New schedule", form)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Create(request.Context(), form, principal.Actor.UserID, principal.SecurityContext())
	if handler.mutationError(writer, request, err, "features/schedules/new", "New schedule", form) {
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/schedules/%d?notice=schedule-created", value.ID), 303)
}

func (handler *Handler) BulkCreate(writer http.ResponseWriter, request *http.Request) {
	form, ok := handler.bulkForm(writer, request)
	if !ok {
		return
	}
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/schedules/bulk", "Bulk create schedules", form)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	result, err := handler.service.CreateMany(request.Context(), form, principal.Actor.UserID, principal.SecurityContext())
	if err != nil {
		if errors.Is(err, domain.ErrInvalidDefinition) {
			form.Errors["form"] = strings.TrimPrefix(err.Error(), domain.ErrInvalidDefinition.Error()+": ")
			handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/schedules/bulk", "Bulk create schedules", form)
			return
		}
		handler.admin.Internal(writer, request, "bulk create schedules", err)
		return
	}
	form.Result = &result
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/schedules/bulk", "Bulk schedule result", form)
}

func (handler *Handler) Show(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.id(writer, request)
	if !ok {
		return
	}
	schedule, err := handler.service.Find(request.Context(), id)
	if handler.readError(writer, request, err, "find schedule") {
		return
	}
	occurrences, err := handler.service.Occurrences(request.Context(), id)
	if err != nil {
		handler.admin.Internal(writer, request, "list schedule occurrences", err)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	handler.admin.RenderPage(writer, request, 200, "features/schedules/show", "Schedule", DetailData{Schedule: schedule, Occurrences: occurrences,
		CanUpdate: principal.Can(PermissionUpdate), CanEnableDisable: principal.Can(PermissionEnableDisable), CanArchive: principal.Can(PermissionArchive)})
}

func (handler *Handler) Edit(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.id(writer, request)
	if !ok {
		return
	}
	value, err := handler.service.Find(request.Context(), id)
	if handler.readError(writer, request, err, "find schedule") {
		return
	}
	form := FormData{ID: value.ID, ExpectedRevision: value.Revision, Name: value.Name, JobKey: value.JobKey, CronExpression: value.CronExpression, Timezone: value.Timezone, Enabled: value.Enabled, Jobs: handler.service.Jobs(), Errors: map[string]string{}}
	handler.admin.RenderPage(writer, request, 200, "features/schedules/edit", "Edit schedule", form)
}

func (handler *Handler) Update(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.id(writer, request)
	if !ok {
		return
	}
	form, ok := handler.form(writer, request)
	if !ok {
		return
	}
	form.ID = id
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, 422, "features/schedules/edit", "Edit schedule", form)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	_, err := handler.service.Update(request.Context(), id, form, principal.Actor.UserID, principal.SecurityContext())
	if handler.mutationError(writer, request, err, "features/schedules/edit", "Edit schedule", form) {
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/schedules/%d?notice=schedule-updated", id), 303)
}

func (handler *Handler) Enable(writer http.ResponseWriter, request *http.Request) {
	handler.state(writer, request, "enable")
}
func (handler *Handler) Disable(writer http.ResponseWriter, request *http.Request) {
	handler.state(writer, request, "disable")
}
func (handler *Handler) Archive(writer http.ResponseWriter, request *http.Request) {
	handler.state(writer, request, "archive")
}

func (handler *Handler) state(writer http.ResponseWriter, request *http.Request, action string) {
	id, ok := handler.id(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	revision, err := strconv.ParseUint(request.PostFormValue("expected_revision"), 10, 64)
	if err != nil {
		http.Error(writer, "Invalid revision.", 422)
		return
	}
	if action == "disable" && request.PostFormValue("confirm_discard") != "yes" {
		http.Error(writer, "Backlog discard acknowledgement is required.", 422)
		return
	}
	if action == "archive" && request.PostFormValue("confirm_archive") != "yes" {
		http.Error(writer, "Archive confirmation is required.", 422)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	if action == "enable" {
		_, err = handler.service.Enable(request.Context(), id, revision, principal.Actor.UserID, principal.SecurityContext())
	} else if action == "disable" {
		_, err = handler.service.Disable(request.Context(), id, revision, principal.Actor.UserID, principal.SecurityContext())
	} else {
		_, err = handler.service.Archive(request.Context(), id, revision, principal.Actor.UserID, principal.SecurityContext())
	}
	if err != nil {
		if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrBacklog) || errors.Is(err, domain.ErrArchived) {
			handler.renderConflict(writer, request, err, fmt.Sprintf("/schedules/%d", id))
			return
		}
		handler.admin.Internal(writer, request, action+" schedule", err)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/schedules/%d?notice=schedule-updated", id), 303)
}

func (handler *Handler) Occurrence(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.id(writer, request)
	if !ok {
		return
	}
	occurrenceID, err := strconv.ParseUint(chi.URLParam(request, "occurrenceID"), 10, 64)
	if err != nil || occurrenceID == 0 {
		handler.admin.NotFound(writer, request)
		return
	}
	schedule, err := handler.service.Find(request.Context(), id)
	if handler.readError(writer, request, err, "find schedule") {
		return
	}
	occurrence, attempts, err := handler.service.FindOccurrence(request.Context(), id, occurrenceID)
	if handler.readError(writer, request, err, "find occurrence") {
		return
	}
	handler.admin.RenderPage(writer, request, 200, "features/schedules/occurrence", "Schedule occurrence", OccurrenceData{schedule, occurrence, attempts})
}

func (handler *Handler) blankForm() FormData {
	return FormData{Timezone: domain.DefaultTimezone, Jobs: handler.service.Jobs(), Errors: map[string]string{}}
}

func (handler *Handler) blankBulkForm() BulkFormData {
	return BulkFormData{Timezone: domain.DefaultTimezone, Jobs: handler.service.Jobs(), SelectedJobs: map[string]bool{}, Errors: map[string]string{}}
}

func (handler *Handler) form(writer http.ResponseWriter, request *http.Request) (FormData, bool) {
	if !webutil.ParseForm(writer, request, maxFormBody) {
		return FormData{}, false
	}
	form := handler.blankForm()
	form.Name = strings.TrimSpace(request.PostFormValue("name"))
	form.JobKey = strings.TrimSpace(request.PostFormValue("job_key"))
	form.CronExpression = strings.TrimSpace(request.PostFormValue("cron_expression"))
	form.Timezone = strings.TrimSpace(request.PostFormValue("timezone"))
	form.Enabled = request.PostFormValue("enabled") == "true"
	form.ExpectedRevision, _ = strconv.ParseUint(request.PostFormValue("expected_revision"), 10, 64)
	handler.rejectManagedFields(request, form.Errors)
	return form, true
}

func (handler *Handler) bulkForm(writer http.ResponseWriter, request *http.Request) (BulkFormData, bool) {
	if !webutil.ParseForm(writer, request, maxFormBody) {
		return BulkFormData{}, false
	}
	form := handler.blankBulkForm()
	for _, jobKey := range request.PostForm["job_keys"] {
		jobKey = strings.TrimSpace(jobKey)
		if jobKey == "" {
			continue
		}
		form.JobKeys = append(form.JobKeys, jobKey)
		form.SelectedJobs[jobKey] = true
	}
	form.CronExpression = strings.TrimSpace(request.PostFormValue("cron_expression"))
	form.Timezone = strings.TrimSpace(request.PostFormValue("timezone"))
	form.Enabled = request.PostFormValue("enabled") == "true"
	if len(form.JobKeys) == 0 {
		form.Errors["jobs"] = "Select at least one job."
	}
	handler.rejectManagedFields(request, form.Errors)
	return form, true
}

func (*Handler) rejectManagedFields(request *http.Request, formErrors map[string]string) {
	for _, key := range []string{"policy_json", "policy_kind", "policy_version", "policy_checksum", "target_kind"} {
		if _, found := request.PostForm[key]; found {
			formErrors["form"] = "Scheduler policy fields are managed by the application."
		}
	}
}

func (handler *Handler) mutationError(writer http.ResponseWriter, request *http.Request, err error, page, title string, form FormData) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, domain.ErrInvalidDefinition) {
		form.Errors["form"] = strings.TrimPrefix(err.Error(), domain.ErrInvalidDefinition.Error()+": ")
		handler.admin.RenderPage(writer, request, 422, page, title, form)
		return true
	}
	if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrBacklog) || errors.Is(err, domain.ErrArchived) {
		backURL := "/schedules"
		if form.ID != 0 {
			backURL = fmt.Sprintf("/schedules/%d", form.ID)
		}
		handler.renderConflict(writer, request, err, backURL)
		return true
	}
	handler.admin.Internal(writer, request, "mutate schedule", err)
	return true
}

func (handler *Handler) renderConflict(writer http.ResponseWriter, request *http.Request, err error, backURL string) {
	message := "Schedule state changed. Reload before trying again."
	if errors.Is(err, domain.ErrBacklog) {
		message = "This schedule has unresolved backlog. Use Disable → Edit → Enable only when intentionally discarding it."
	} else if errors.Is(err, domain.ErrArchived) {
		message = "Archived schedules are read-only."
	}
	handler.admin.RenderPage(writer, request, http.StatusConflict, "conflict", "Conflict", struct{ Message, BackURL string }{message, backURL})
}

func (handler *Handler) id(writer http.ResponseWriter, request *http.Request) (uint64, bool) {
	id, ok := webutil.RouteID(request)
	if !ok {
		handler.admin.NotFound(writer, request)
	}
	return id, ok
}
func (handler *Handler) principal(writer http.ResponseWriter, request *http.Request) (browserauth.Principal, bool) {
	value, ok := browserauth.CurrentPrincipal(request.Context())
	if !ok {
		handler.admin.Internal(writer, request, "schedule handler", errors.New("principal missing"))
	}
	return value, ok
}
func (handler *Handler) readError(writer http.ResponseWriter, request *http.Request, err error, operation string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		handler.admin.NotFound(writer, request)
	} else {
		handler.admin.Internal(writer, request, operation, err)
	}
	return true
}
