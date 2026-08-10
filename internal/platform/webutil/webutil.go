package webutil

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func RouteID(request *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(chi.URLParam(request, "id"), 10, 64)
	return id, err == nil && id != 0
}

func Page(request *http.Request) int {
	page, err := strconv.Atoi(request.URL.Query().Get("page"))
	if err != nil || page < 1 {
		return 1
	}
	return page
}

func ParseForm(writer http.ResponseWriter, request *http.Request, maxBytes int64) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, maxBytes)
	if err := request.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(writer, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		} else {
			http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		}
		return false
	}
	return true
}
