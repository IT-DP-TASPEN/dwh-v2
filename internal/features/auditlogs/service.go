package auditlogs

import (
	"context"
	"fmt"
	"strings"

	"github.com/ibldzn/go-admin/internal/platform/pagination"
)

type store interface {
	Count(context.Context, string) (int64, error)
	List(context.Context, string, int, int) ([]Record, error)
	Find(context.Context, uint64) (Record, error)
}

type Service struct {
	store store
}

func NewService(store store) *Service {
	return &Service{store: store}
}

func (service *Service) List(ctx context.Context, action string, page int) (Page, error) {
	action = strings.TrimSpace(action)
	total, err := service.store.Count(ctx, action)
	if err != nil {
		return Page{}, fmt.Errorf("count audit history: %w", err)
	}
	pageInfo := pagination.New(page, PageSize, total)
	rows, err := service.store.List(ctx, action, pageInfo.PerPage, pageInfo.Offset())
	if err != nil {
		return Page{}, fmt.Errorf("list audit history: %w", err)
	}
	return Page{Rows: rows, Action: action, Pagination: pageInfo}, nil
}

func (service *Service) Find(ctx context.Context, id uint64) (Record, error) {
	if id == 0 {
		return Record{}, ErrNotFound
	}
	return service.store.Find(ctx, id)
}
