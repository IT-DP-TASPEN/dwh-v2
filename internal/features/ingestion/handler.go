package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/ibldzn/go-admin/internal/browserauth"
	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

const maxRunFormBody = 32 << 10

type runService interface {
	ListRuns(context.Context, RunFilter, int) (RunPage, error)
	FindRun(context.Context, uint64) (RunDetail, error)
	OverviewRuns(context.Context) (RunOverview, error)
	OverviewSources(context.Context) (SourceOverview, error)
	OverviewSchedules(context.Context) (ScheduleOverview, error)
	ActiveRunID(context.Context, string) (uint64, bool, error)
}

type coordinator interface {
	SubmitRunAll(context.Context, core.CalendarDate, core.CalendarDate, ingestionrun.Trigger, string, *uint64) (uint64, error)
	Cancel(context.Context, uint64, string, securityctx.Requester) error
	RecoverAbandoned(context.Context, uint64, string, time.Time, string, securityctx.Requester) error
}

type Handler struct {
	admin       *adminshell.Shell
	service     runService
	coordinator coordinator
}

func NewHandler(admin *adminshell.Shell, service runService, coordinator coordinator) *Handler {
	return &Handler{admin: admin, service: service, coordinator: coordinator}
}

func (handler *Handler) Overview(writer http.ResponseWriter, request *http.Request) {
	handler.renderOverview(writer, request, false)
}

func (handler *Handler) Summary(writer http.ResponseWriter, request *http.Request) {
	handler.renderOverview(writer, request, true)
}

func (handler *Handler) renderOverview(writer http.ResponseWriter, request *http.Request, partial bool) {
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	data := OverviewData{CanRunAll: principal.Can(PermissionRunAll) && principal.Can(PermissionView)}
	var err error
	if principal.Can(PermissionView) {
		value, queryErr := handler.service.OverviewRuns(request.Context())
		err = queryErr
		if err == nil {
			data.Runs = &value
		}
	}
	if err == nil && principal.Can("sources.view") {
		value, queryErr := handler.service.OverviewSources(request.Context())
		err = queryErr
		if err == nil {
			data.Sources = &value
		}
	}
	if err == nil && principal.Can("schedules.view") {
		value, queryErr := handler.service.OverviewSchedules(request.Context())
		err = queryErr
		if err == nil {
			data.Schedules = &value
		}
	}
	if err != nil {
		handler.admin.Internal(writer, request, "load ingestion overview", err)
		return
	}
	pageData, ok := handler.admin.PageData(request, "Ingestion Overview", data)
	if !ok {
		handler.admin.Internal(writer, request, "prepare ingestion overview", errors.New("principal missing"))
		return
	}
	name := "admin"
	if partial {
		name = "ingestion-summary"
	}
	if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/ingestion/index", name, pageData); err != nil {
		handler.admin.Internal(writer, request, "render ingestion overview", err)
	}
}

func (handler *Handler) Runs(writer http.ResponseWriter, request *http.Request) {
	filter := RunFilter{Job: request.URL.Query().Get("job"), Status: request.URL.Query().Get("status"), Kind: request.URL.Query().Get("kind"), Trigger: request.URL.Query().Get("trigger")}
	page, err := handler.service.ListRuns(request.Context(), filter, webutil.Page(request))
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid ") {
			http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		handler.admin.Internal(writer, request, "list ingestion runs", err)
		return
	}
	pageData, ok := handler.admin.PageData(request, "Ingestion Runs", page)
	if !ok {
		handler.admin.Internal(writer, request, "prepare run history", errors.New("principal missing"))
		return
	}
	name := "admin"
	if request.Header.Get("HX-Request") == "true" {
		name = "runs-table"
	}
	if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/ingestion/runs", name, pageData); err != nil {
		handler.admin.Internal(writer, request, "render run history", err)
	}
}

func (handler *Handler) Run(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok {
		return
	}
	detail, err := handler.service.FindRun(request.Context(), id)
	if err != nil {
		handler.readError(writer, request, "find ingestion run", err)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	detail.CanCancel = principal.Can(PermissionCancel) && !detail.Run.Terminal
	detail.CanRecover = principal.Can(PermissionRecoverAbandoned) && detail.Run.Kind != string(ingestionrun.KindRunAllParent) && detail.Run.Status.Key == string(ingestionrun.StatusRunning)
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/ingestion/show", "Run Details", detail)
}

func (handler *Handler) RunStatus(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok {
		return
	}
	detail, err := handler.service.FindRun(request.Context(), id)
	if err != nil {
		handler.readError(writer, request, "refresh ingestion run", err)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	detail.CanCancel = principal.Can(PermissionCancel) && !detail.Run.Terminal
	detail.CanRecover = principal.Can(PermissionRecoverAbandoned) && detail.Run.Kind != string(ingestionrun.KindRunAllParent) && detail.Run.Status.Key == string(ingestionrun.StatusRunning)
	pageData, ok := handler.admin.PageData(request, "Run Details", detail)
	if !ok {
		handler.admin.Internal(writer, request, "prepare run status", errors.New("principal missing"))
		return
	}
	if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/ingestion/show", "run-status", pageData); err != nil {
		handler.admin.Internal(writer, request, "render run status", err)
	}
}

func (handler *Handler) RunAllPage(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/ingestion/run_all", "Run All", RunAllForm{Errors: map[string]string{}})
}

func (handler *Handler) SubmitRunAll(writer http.ResponseWriter, request *http.Request) {
	if !webutil.ParseForm(writer, request, maxRunFormBody) {
		return
	}
	form := RunAllForm{From: strings.TrimSpace(request.PostFormValue("from")), To: strings.TrimSpace(request.PostFormValue("to")), Errors: map[string]string{}}
	from, err := core.ParseCalendarDate(form.From)
	if err != nil {
		form.Errors["from"] = "Enter a valid From date."
	}
	to, err := core.ParseCalendarDate(form.To)
	if err != nil {
		form.Errors["to"] = "Enter a valid To date."
	}
	if len(form.Errors) == 0 && from.String() > to.String() {
		form.Errors["to"] = "To must not be before From."
	}
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/ingestion/run_all", "Run All", form)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	actor := principal.Actor.UserID
	id, err := handler.coordinator.SubmitRunAll(request.Context(), from, to, ingestionrun.TriggerDirect, "web:"+middleware.GetReqID(request.Context()), &actor)
	if err != nil {
		handler.admin.Internal(writer, request, "submit Run All", err)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/runs/%d?notice=run-all-submitted", id), http.StatusSeeOther)
}

func (handler *Handler) Cancel(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxRunFormBody) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	reason := strings.TrimSpace(request.PostFormValue("reason"))
	if len([]rune(reason)) > 255 {
		handler.conflict(writer, request, "Cancellation reason is too long.", fmt.Sprintf("/runs/%d", id))
		return
	}
	if err := handler.coordinator.Cancel(request.Context(), id, reason, principal.SecurityContext()); err != nil {
		if errors.Is(err, ingestionrun.ErrTransition) {
			handler.conflict(writer, request, "Run state changed. Refresh before trying again.", fmt.Sprintf("/runs/%d", id))
			return
		}
		handler.readError(writer, request, "cancel ingestion run", err)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/runs/%d?notice=cancellation-requested", id), http.StatusSeeOther)
}

func (handler *Handler) RecoverAbandoned(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxRunFormBody) {
		return
	}
	if request.PostFormValue("confirm_worker_stopped") != "yes" {
		handler.conflict(writer, request, "Confirm that the owning worker process is permanently stopped.", fmt.Sprintf("/runs/%d", id))
		return
	}
	owner, reason := strings.TrimSpace(request.PostFormValue("expected_owner")), strings.TrimSpace(request.PostFormValue("reason"))
	heartbeat, err := time.Parse(time.RFC3339Nano, request.PostFormValue("expected_heartbeat"))
	if owner == "" || err != nil || reason == "" || len([]rune(reason)) > 500 {
		handler.conflict(writer, request, "Recovery evidence or reason is invalid. Refresh and try again.", fmt.Sprintf("/runs/%d", id))
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	err = handler.coordinator.RecoverAbandoned(request.Context(), id, owner, heartbeat, reason, principal.SecurityContext())
	if errors.Is(err, ingestionrun.ErrTransition) {
		handler.conflict(writer, request, "Run ownership changed. Refresh before trying again.", fmt.Sprintf("/runs/%d", id))
		return
	}
	if err != nil {
		handler.readError(writer, request, "recover abandoned run", err)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/runs/%d?notice=run-abandoned", id), http.StatusSeeOther)
}

func (handler *Handler) principal(writer http.ResponseWriter, request *http.Request) (browserauth.Principal, bool) {
	principal, ok := browserauth.CurrentPrincipal(request.Context())
	if !ok {
		handler.admin.Internal(writer, request, "ingestion handler", errors.New("principal missing"))
	}
	return principal, ok
}

func (handler *Handler) routeID(writer http.ResponseWriter, request *http.Request) (uint64, bool) {
	id, ok := webutil.RouteID(request)
	if !ok {
		handler.admin.NotFound(writer, request)
	}
	return id, ok
}

func (handler *Handler) readError(writer http.ResponseWriter, request *http.Request, operation string, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		handler.admin.NotFound(writer, request)
		return
	}
	handler.admin.Internal(writer, request, operation, err)
}

func (handler *Handler) conflict(writer http.ResponseWriter, request *http.Request, message, backURL string) {
	handler.admin.RenderPage(writer, request, http.StatusConflict, "conflict", "Conflict", struct{ Message, BackURL string }{message, backURL})
}
