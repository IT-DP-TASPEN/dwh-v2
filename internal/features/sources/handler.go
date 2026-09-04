package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ibldzn/go-admin/internal/browserauth"
	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

const maxFormBody = 32 << 10

type sourceService interface {
	List(context.Context, bool) ([]Source, error)
	AuthProfiles(context.Context) ([]AuthProfileOption, error)
	Find(string) (core.JobDefinition, bool)
	SetEnabled(context.Context, string, bool, bool, uint64, ...securityctx.Requester) error
	SetAuthProfile(context.Context, string, *uint64, *uint64, uint64, ...securityctx.Requester) error
}

type submitter interface {
	SubmitManual(context.Context, string, ingestionrun.Parameters, ingestionrun.Trigger, string, securityctx.Requester) (uint64, error)
}

type Handler struct {
	admin       *adminshell.Shell
	service     sourceService
	coordinator submitter
}

type ListData struct {
	Rows              []Source
	AuthProfiles      []AuthProfileOption
	CanManage, CanRun bool
	CanViewRuns       bool
}

type SourceRowData struct {
	Source       Source
	AuthProfiles []AuthProfileOption
	CanManage    bool
	CanRun       bool
	CanViewRuns  bool
	Error        string
}

func (data ListData) RowViews() []SourceRowData {
	rows := make([]SourceRowData, len(data.Rows))
	for index, source := range data.Rows {
		rows[index] = SourceRowData{Source: source, AuthProfiles: data.AuthProfiles, CanManage: data.CanManage, CanRun: data.CanRun, CanViewRuns: data.CanViewRuns}
	}
	return rows
}

type RunForm struct {
	Job      core.JobDefinition
	From, To string
	Errors   map[string]string
}

func NewHandler(admin *adminshell.Shell, service sourceService, coordinator submitter) *Handler {
	return &Handler{admin: admin, service: service, coordinator: coordinator}
}

func (handler *Handler) Sources(writer http.ResponseWriter, request *http.Request) {
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	canViewRuns := principal.Can("ingestion.view")
	rows, err := handler.service.List(request.Context(), canViewRuns)
	if err != nil {
		handler.admin.Internal(writer, request, "list sources", err)
		return
	}
	profiles, err := handler.service.AuthProfiles(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "list source Auth Profiles", err)
		return
	}
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/sources/index", "Sources", ListData{
		Rows: rows, AuthProfiles: profiles, CanManage: principal.Can(PermissionManage), CanRun: principal.Can("ingestion.run"), CanViewRuns: canViewRuns,
	})
}

func (handler *Handler) SetAuthProfile(writer http.ResponseWriter, request *http.Request) {
	job, ok := handler.job(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	expected, validExpected := optionalID(request.PostFormValue("expected_profile_id"))
	target, validTarget := optionalID(request.PostFormValue("profile_id"))
	if !validExpected || !validTarget {
		handler.authProfileResult(writer, request, job.Key, principal, "Invalid Auth Profile selection; persisted selection restored.", http.StatusUnprocessableEntity)
		return
	}
	if err := handler.service.SetAuthProfile(request.Context(), job.Key, expected, target, principal.Actor.UserID, principal.SecurityContext()); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidAuthProfile) {
			handler.authProfileResult(writer, request, job.Key, principal, err.Error()+"; persisted selection restored.", http.StatusConflict)
			return
		}
		if request.Header.Get("HX-Request") == "true" {
			handler.authProfileResult(writer, request, job.Key, principal, "Auth Profile assignment could not be saved; persisted selection restored.", http.StatusInternalServerError)
			return
		}
		handler.admin.Internal(writer, request, "assign source Auth Profile", err)
		return
	}
	if request.Header.Get("HX-Request") == "true" {
		handler.authProfileResult(writer, request, job.Key, principal, "", http.StatusOK)
		return
	}
	http.Redirect(writer, request, "/sources?notice=source-auth-profile-updated", http.StatusSeeOther)
}

func (handler *Handler) authProfileResult(writer http.ResponseWriter, request *http.Request, key string, principal browserauth.Principal, message string, fallbackStatus int) {
	if request.Header.Get("HX-Request") != "true" {
		http.Error(writer, message, fallbackStatus)
		return
	}
	canViewRuns := principal.Can("ingestion.view")
	rows, err := handler.service.List(request.Context(), canViewRuns)
	if err != nil {
		handler.admin.Internal(writer, request, "reload source after Auth Profile assignment", err)
		return
	}
	profiles, err := handler.service.AuthProfiles(request.Context())
	if err != nil {
		handler.admin.Internal(writer, request, "reload source Auth Profiles after assignment", err)
		return
	}
	for _, source := range rows {
		if source.Job.Key == key {
			data := SourceRowData{Source: source, AuthProfiles: profiles, CanManage: principal.Can(PermissionManage), CanRun: principal.Can("ingestion.run"), CanViewRuns: canViewRuns, Error: message}
			if err := handler.admin.RenderPartial(writer, http.StatusOK, "features/sources/index", "source-row", data); err != nil {
				handler.admin.Internal(writer, request, "render source after Auth Profile assignment", err)
			}
			return
		}
	}
	handler.admin.NotFound(writer, request)
}

func optionalID(value string) (*uint64, bool) {
	if value == "" {
		return nil, true
	}
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 {
		return nil, false
	}
	return &id, true
}

func (handler *Handler) RunPage(writer http.ResponseWriter, request *http.Request) {
	job, ok := handler.job(writer, request)
	if ok {
		handler.admin.RenderPage(writer, request, http.StatusOK, "features/sources/run", "Run source", RunForm{Job: job, Errors: map[string]string{}})
	}
}

func (handler *Handler) Submit(writer http.ResponseWriter, request *http.Request) {
	job, ok := handler.job(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	form := RunForm{Job: job, From: strings.TrimSpace(request.PostFormValue("from")), To: strings.TrimSpace(request.PostFormValue("to")), Errors: map[string]string{}}
	for _, forbidden := range []string{"branch", "location", "location_id", "account_code"} {
		if _, found := request.PostForm[forbidden]; found {
			form.Errors["form"] = "Internal source dimensions cannot be submitted."
		}
	}
	parameters, validation := Parameters(job, form.From, form.To)
	for key, value := range validation {
		form.Errors[key] = value
	}
	if len(form.Errors) != 0 {
		handler.admin.RenderPage(writer, request, http.StatusUnprocessableEntity, "features/sources/run", "Run source", form)
		return
	}
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	id, err := handler.coordinator.SubmitManual(request.Context(), job.Key, parameters, ingestionrun.TriggerDirect, "web:"+middleware.GetReqID(request.Context()), principal.SecurityContext())
	if errors.Is(err, ingestionrun.ErrJobBusy) || errors.Is(err, ingestionrun.ErrSourceDisabled) || errors.Is(err, ingestionrun.ErrSourceConfigurationRequired) {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		handler.admin.Internal(writer, request, "submit source run", err)
		return
	}
	http.Redirect(writer, request, fmt.Sprintf("/runs/%d", id), http.StatusSeeOther)
}

func (handler *Handler) Enable(writer http.ResponseWriter, request *http.Request) {
	handler.setEnabled(writer, request, true)
}
func (handler *Handler) Disable(writer http.ResponseWriter, request *http.Request) {
	handler.setEnabled(writer, request, false)
}

func (handler *Handler) setEnabled(writer http.ResponseWriter, request *http.Request, enabled bool) {
	job, ok := handler.job(writer, request)
	if !ok || !webutil.ParseForm(writer, request, maxFormBody) {
		return
	}
	expected := request.PostFormValue("expected_enabled") == "true"
	principal, ok := handler.principal(writer, request)
	if !ok {
		return
	}
	if err := handler.service.SetEnabled(request.Context(), job.Key, expected, enabled, principal.Actor.UserID, principal.SecurityContext()); err != nil {
		if errors.Is(err, ErrConflict) {
			http.Error(writer, "Source state changed; reload and try again.", http.StatusConflict)
			return
		}
		handler.admin.Internal(writer, request, "update source", err)
		return
	}
	http.Redirect(writer, request, "/sources?notice=source-updated", http.StatusSeeOther)
}

func (handler *Handler) job(writer http.ResponseWriter, request *http.Request) (core.JobDefinition, bool) {
	job, found := handler.service.Find(chi.URLParam(request, "jobKey"))
	if !found {
		handler.admin.NotFound(writer, request)
	}
	return job, found
}

func (handler *Handler) principal(writer http.ResponseWriter, request *http.Request) (browserauth.Principal, bool) {
	principal, ok := browserauth.CurrentPrincipal(request.Context())
	if !ok {
		handler.admin.Internal(writer, request, "source handler", errors.New("principal missing"))
	}
	return principal, ok
}
