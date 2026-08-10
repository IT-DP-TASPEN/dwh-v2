package dashboard

import (
	"net/http"

	"github.com/ibldzn/go-admin/internal/platform/adminshell"
)

type Handler struct {
	admin *adminshell.Shell
}

func NewHandler(admin *adminshell.Shell) *Handler {
	return &Handler{admin: admin}
}

func (handler *Handler) Index(writer http.ResponseWriter, request *http.Request) {
	handler.admin.RenderPage(writer, request, http.StatusOK, "features/dashboard/index", "Dashboard", nil)
}
