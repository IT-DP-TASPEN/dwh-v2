package sources

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ibldzn/go-admin/internal/browserauth"
	core "github.com/ibldzn/go-admin/internal/ingestion"
	"github.com/ibldzn/go-admin/internal/ingestionrun"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/webutil"
)

const maxFormBody = 32 << 10

type sourceService interface {
	List(context.Context, bool) ([]Source, error)
	Find(string) (core.JobDefinition, bool)
	SetEnabled(context.Context, string, bool, bool, uint64) error
}

type submitter interface {
	Submit(context.Context, string, ingestionrun.Parameters, ingestionrun.Trigger, string, *uint64) (uint64, error)
}

type Handler struct {
	admin       *adminshell.Shell
	service     sourceService
	coordinator submitter
}

type ListData struct {
	Rows              []Source
	CanManage, CanRun bool
	CanViewRuns       bool
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
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/sources/index", "Sources", ListData{
		Rows: rows, CanManage: principal.Can(PermissionManage), CanRun: principal.Can("ingestion.run"), CanViewRuns: canViewRuns,
	})
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
	actor := principal.Actor.UserID
	id, err := handler.coordinator.Submit(request.Context(), job.Key, parameters, ingestionrun.TriggerDirect, "web:"+middleware.GetReqID(request.Context()), &actor)
	if errors.Is(err, ingestionrun.ErrJobBusy) || errors.Is(err, ingestionrun.ErrSourceDisabled) {
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
	if err := handler.service.SetEnabled(request.Context(), job.Key, expected, enabled, principal.Actor.UserID); err != nil {
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
