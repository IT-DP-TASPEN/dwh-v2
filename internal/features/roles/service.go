package roles

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type store interface {
	List(context.Context) ([]Record, error)
	FindByID(context.Context, uint64) (Record, error)
	Create(context.Context, securityctx.Requester, string, string, time.Time) (Record, error)
	UpdateName(context.Context, securityctx.Requester, uint64, string, time.Time) (Record, error)
	Delete(context.Context, securityctx.Requester, uint64, time.Time) error
	ListPermissionKeys(context.Context, uint64) ([]string, error)
	ReplacePermissions(context.Context, securityctx.Requester, uint64, []string, []string, time.Time) error
}

type Service struct {
	store       store
	definitions []access.PermissionDefinition
}

func NewService(store store, definitions []access.PermissionDefinition) *Service {
	return &Service{store: store, definitions: append([]access.PermissionDefinition(nil), definitions...)}
}

func (service *Service) List(ctx context.Context) ([]Record, error) {
	rows, err := service.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list managed roles: %w", err)
	}
	return rows, nil
}

func (service *Service) Find(ctx context.Context, id uint64) (Detail, error) {
	if id == 0 {
		return Detail{}, ErrNotFound
	}
	role, err := service.store.FindByID(ctx, id)
	if err != nil {
		return Detail{}, fmt.Errorf("find managed role: %w", err)
	}
	if access.IsAdminRole(role.Slug) {
		return Detail{Role: role}, nil
	}
	selected := []string(nil)
	selected, err = service.store.ListPermissionKeys(ctx, id)
	if err != nil {
		return Detail{}, fmt.Errorf("list managed role permissions: %w", err)
	}
	return Detail{Role: role, PermissionGroups: service.permissionGroups(selected)}, nil
}

func (service *Service) Create(ctx context.Context, requester securityctx.Requester, input CreateInput, now time.Time) (Record, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = NormalizeSlug(input.Slug)
	validation := ValidationErrors{}
	if err := validateName(input.Name); err != nil {
		validation["name"] = err.Error()
	}
	if err := ValidateSlug(input.Slug); err != nil {
		validation["slug"] = err.Error()
	}
	if len(validation) != 0 {
		return Record{}, validation
	}
	created, err := service.store.Create(ctx, requester, input.Name, input.Slug, now.UTC())
	if err != nil {
		return Record{}, fmt.Errorf("create managed role: %w", err)
	}
	return created, nil
}

func (service *Service) Update(ctx context.Context, requester securityctx.Requester, id uint64, name string, now time.Time) (Record, error) {
	name = strings.TrimSpace(name)
	if err := validateName(name); err != nil {
		return Record{}, ValidationErrors{"name": err.Error()}
	}
	role, err := service.store.FindByID(ctx, id)
	if err != nil {
		return Record{}, fmt.Errorf("find updated role: %w", err)
	}
	if role.IsSystem {
		return Record{}, ErrProtectedRole
	}
	updated, err := service.store.UpdateName(ctx, requester, id, name, now.UTC())
	if err != nil {
		return Record{}, fmt.Errorf("update managed role: %w", err)
	}
	return updated, nil
}

func (service *Service) Delete(ctx context.Context, requester securityctx.Requester, id uint64, now time.Time) error {
	role, err := service.store.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("find deleted role: %w", err)
	}
	if role.IsSystem {
		return ErrProtectedRole
	}
	if role.UserCount != 0 {
		return ErrRoleAssigned
	}
	if err := service.store.Delete(ctx, requester, id, now.UTC()); err != nil {
		return fmt.Errorf("delete managed role: %w", err)
	}
	return nil
}

func (service *Service) ReplacePermissions(ctx context.Context, requester securityctx.Requester, roleID uint64, keys []string, now time.Time) error {
	role, err := service.store.FindByID(ctx, roleID)
	if err != nil {
		return fmt.Errorf("find permission role: %w", err)
	}
	if access.IsAdminRole(role.Slug) {
		return ErrAdminPermissions
	}
	if !requester.IsEffectiveAdmin() && requester.EffectiveRoleID == roleID {
		return ErrSelfRolePermissions
	}

	canonical := make([]string, 0, len(service.definitions))
	known := make(map[string]struct{}, len(service.definitions))
	for _, definition := range service.definitions {
		canonical = append(canonical, definition.Key)
		known[definition.Key] = struct{}{}
	}
	selected := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownPermission, key)
		}
		if _, duplicate := seen[key]; !duplicate {
			seen[key] = struct{}{}
			selected = append(selected, key)
		}
	}
	if err := service.store.ReplacePermissions(ctx, requester, roleID, canonical, selected, now.UTC()); err != nil {
		return fmt.Errorf("replace managed role permissions: %w", err)
	}
	return nil
}

func (service *Service) permissionGroups(selectedKeys []string) []PermissionGroup {
	selected := make(map[string]struct{}, len(selectedKeys))
	for _, key := range selectedKeys {
		selected[key] = struct{}{}
	}
	groups := make([]PermissionGroup, 0, 4)
	indexes := make(map[string]int)
	for _, definition := range service.definitions {
		index, ok := indexes[definition.Group]
		if !ok {
			index = len(groups)
			indexes[definition.Group] = index
			groups = append(groups, PermissionGroup{Name: definition.Group})
		}
		_, checked := selected[definition.Key]
		groups[index].Permissions = append(groups[index].Permissions, PermissionOption{
			Key: definition.Key, Name: definition.Name, Description: definition.Description, Selected: checked,
		})
	}
	return groups
}
