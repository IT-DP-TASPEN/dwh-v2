package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
	RunAllChildren(context.Context, uint64) (RunChildren, error)
	SchedulerWave(context.Context, time.Time) (SchedulerWaveDetail, error)
	FindRun(context.Context, uint64) (RunDetail, error)
	OverviewRuns(context.Context) (RunOverview, error)
	OverviewSources(context.Context) (SourceOverview, error)
	OverviewSchedules(context.Context) (ScheduleOverview, error)
	NeedsAttention(context.Context, bool, bool, int) ([]AttentionItem, error)
	ActiveRunID(context.Context, string) (uint64, bool, error)
}

type coordinator interface {
	SubmitRunAllManual(context.Context, core.CalendarDate, core.CalendarDate, ingestionrun.Trigger, string, securityctx.Requester) (uint64, error)
	Cancel(context.Context, uint64, string, securityctx.Requester) error
	RecoverAbandoned(context.Context, uint64, string, time.Time, string, securityctx.Requester) error
	RuntimeSettings(context.Context) (ingestionrun.RuntimeSettings, error)
	UpdateRuntimeSettings(context.Context, ingestionrun.RuntimeSettings, ingestionrun.RuntimeSettings, securityctx.Requester) (bool, error)
	RunningJobs(context.Context) (int, error)
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
	includeRuns, includeSchedules := principal.Can(PermissionView), principal.Can("schedules.view")
	data.AttentionVisible = includeRuns || includeSchedules
	if err == nil && data.AttentionVisible {
		data.Attention, err = handler.service.NeedsAttention(request.Context(), includeRuns, includeSchedules, 5)
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

func (handler *Handler) RunAllChildren(writer http.ResponseWriter, request *http.Request) {
	id, ok := handler.routeID(writer, request)
	if !ok {
		return
	}
	children, err := handler.service.RunAllChildren(request.Context(), id)
	if err != nil {
		handler.readError(writer, request, "list Run All children", err)
		return
	}
	pageData, ok := handler.admin.PageData(request, "Run All children", children)
	if !ok {
		handler.admin.Internal(writer, request, "prepare Run All children", errors.New("principal missing"))
		return
	}
	if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/ingestion/runs", "run-all-children", pageData); err != nil {
		handler.admin.Internal(writer, request, "render Run All children", err)
	}
}

func (handler *Handler) SchedulerWave(writer http.ResponseWriter, request *http.Request) {
	scheduledFor, err := parseSchedulerWaveTime(request.URL.Query().Get("scheduled_for"))
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	wave, err := handler.service.SchedulerWave(request.Context(), scheduledFor)
	if err != nil {
		handler.readError(writer, request, "load scheduler wave", err)
		return
	}
	pageData, ok := handler.admin.PageData(request, "Scheduler wave", wave)
	if !ok {
		handler.admin.Internal(writer, request, "prepare scheduler wave", errors.New("principal missing"))
		return
	}
	if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/ingestion/runs", "scheduler-wave-attempts", pageData); err != nil {
		handler.admin.Internal(writer, request, "render scheduler wave", err)
	}
}

func parseSchedulerWaveTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Nanosecond()%1000 != 0 {
		return time.Time{}, fmt.Errorf("invalid scheduled_for")
	}
	return parsed.UTC(), nil
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
	detail.SwapCancelAction = actionCapabilityChanged(request.URL.Query().Get("can_cancel"), detail.CanCancel)
	detail.SwapRecoverAction = actionCapabilityChanged(request.URL.Query().Get("can_recover"), detail.CanRecover)
	pageData, ok := handler.admin.PageData(request, "Run Details", detail)
	if !ok {
		handler.admin.Internal(writer, request, "prepare run status", errors.New("principal missing"))
		return
	}
	if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/ingestion/show", "run-status-poll", pageData); err != nil {
		handler.admin.Internal(writer, request, "render run status", err)
	}
}

func actionCapabilityChanged(rendered string, current bool) bool {
	return rendered != strconv.FormatBool(current)
}

func (handler *Handler) RunAllPage(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/ingestion/run_all", "Run All", RunAllForm{Errors: map[string]string{}, JobCount: core.CanonicalJobCount})
}

func (handler *Handler) SubmitRunAll(writer http.ResponseWriter, request *http.Request) {
	if !webutil.ParseForm(writer, request, maxRunFormBody) {
		return
	}
	form := RunAllForm{From: strings.TrimSpace(request.PostFormValue("from")), To: strings.TrimSpace(request.PostFormValue("to")), Errors: map[string]string{}, JobCount: core.CanonicalJobCount}
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
	id, err := handler.coordinator.SubmitRunAllManual(request.Context(), from, to, ingestionrun.TriggerDirect, "web:"+middleware.GetReqID(request.Context()), principal.SecurityContext())
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

const runtimeSettingsPath = "/ingestion/runtime-settings"

func (handler *Handler) RuntimeSettingsPage(writer http.ResponseWriter, request *http.Request) {
	settings, err := handler.coordinator.RuntimeSettings(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "load ingestion runtime settings", err)
		return
	}
	handler.renderRuntimeSettings(writer, request, http.StatusOK, newRuntimeSettingsForm(settings))
}

func (handler *Handler) UpdateRuntimeSettings(writer http.ResponseWriter, request *http.Request) {
	if !browserauth.RequireRecentMFA(writer, request, runtimeSettingsPath) || !webutil.ParseForm(writer, request, maxRunFormBody) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	form := RuntimeSettingsForm{Errors: map[string]string{}}
	var target ingestionrun.RuntimeSettings
	form.MaxRunningJobs, target.MaxRunningJobs = runtimeLimit(request, "max_running_jobs", form.Errors)
	form.FixedMemberConcurrency, target.FixedMemberConcurrency = runtimeLimit(request, "fixed_member_concurrency", form.Errors)
	form.DetailConcurrency, target.DetailConcurrency = runtimeLimit(request, "detail_concurrency", form.Errors)
	expectedErrors := map[string]string{}
	_, form.Expected.MaxRunningJobs = runtimeLimit(request, "expected_max_running_jobs", expectedErrors)
	_, form.Expected.FixedMemberConcurrency = runtimeLimit(request, "expected_fixed_member_concurrency", expectedErrors)
	_, form.Expected.DetailConcurrency = runtimeLimit(request, "expected_detail_concurrency", expectedErrors)
	if len(expectedErrors) != 0 {
		handler.runtimeSettingsConflict(writer, request)
		return
	}
	if len(form.Errors) != 0 {
		handler.renderRuntimeSettings(writer, request, http.StatusUnprocessableEntity, form)
		return
	}
	_, err := handler.coordinator.UpdateRuntimeSettings(request.Context(), form.Expected, target, principal.SecurityContext())
	if errors.Is(err, ingestionrun.ErrRuntimeSettingsConflict) {
		handler.runtimeSettingsConflict(writer, request)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "update ingestion runtime settings", err)
		return
	}
	http.Redirect(writer, request, runtimeSettingsPath+"?notice=ingestion-runtime-settings-updated", http.StatusSeeOther)
}

// runtimeSettingsConflict discards the stale submission and re-renders the
// persisted values so the operator reviews them before resubmitting.
func (handler *Handler) runtimeSettingsConflict(writer http.ResponseWriter, request *http.Request) {
	settings, err := handler.coordinator.RuntimeSettings(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "reload ingestion runtime settings", err)
		return
	}
	form := newRuntimeSettingsForm(settings)
	form.Errors["form"] = "Runtime settings changed while this page was open. Review the current values and submit again."
	handler.renderRuntimeSettings(writer, request, http.StatusConflict, form)
}

func (handler *Handler) renderRuntimeSettings(writer http.ResponseWriter, request *http.Request, status int, form RuntimeSettingsForm) {
	running, err := handler.coordinator.RunningJobs(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "count running ingestion jobs", err)
		return
	}
	form.Running, form.Min, form.Max = running, ingestionrun.MinRuntimeLimit, ingestionrun.MaxRuntimeLimit
	handler.admin.RenderPage(writer, request, status, "features/ingestion/runtime_settings", "Runtime Settings", form)
}

func newRuntimeSettingsForm(settings ingestionrun.RuntimeSettings) RuntimeSettingsForm {
	return RuntimeSettingsForm{Expected: settings, Errors: map[string]string{},
		MaxRunningJobs: strconv.Itoa(settings.MaxRunningJobs), FixedMemberConcurrency: strconv.Itoa(settings.FixedMemberConcurrency), DetailConcurrency: strconv.Itoa(settings.DetailConcurrency)}
}

func runtimeLimit(request *http.Request, field string, errs map[string]string) (string, int) {
	raw := strings.TrimSpace(request.PostFormValue(field))
	value, err := strconv.Atoi(raw)
	if err != nil || !ingestionrun.ValidRuntimeLimit(value) {
		errs[field] = fmt.Sprintf("Enter a whole number from %d to %d.", ingestionrun.MinRuntimeLimit, ingestionrun.MaxRuntimeLimit)
	}
	return raw, value
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
