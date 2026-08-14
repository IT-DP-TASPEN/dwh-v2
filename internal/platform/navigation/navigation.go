package navigation

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/ibldzn/go-admin/internal/access"
)

type MatchMode string

const (
	MatchExact  MatchMode = "exact"
	MatchPrefix MatchMode = "prefix"
)

type Group struct {
	Key   string
	Label string
	Items []Item
}

type Item struct {
	Key            string
	Label          string
	Icon           string
	Path           string
	Permission     string
	AnyPermissions []string
	Match          MatchMode
	Children       []Item
}

type GroupView struct {
	Key   string
	Label string
	Items []ItemView
}

type ItemView struct {
	Key      string
	Label    string
	Icon     string
	Path     string
	Depth    int
	Active   bool
	Open     bool
	Children []ItemView
}

type Registry struct {
	groups []Group
}

func NewRegistry(groups []Group, permissions []access.PermissionDefinition) (*Registry, error) {
	knownPermissions := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		knownPermissions[permission.Key] = struct{}{}
	}

	groupKeys := make(map[string]struct{}, len(groups))
	itemKeys := make(map[string]struct{})
	for groupIndex, group := range groups {
		if strings.TrimSpace(group.Key) == "" {
			return nil, fmt.Errorf("navigation group %d has empty key", groupIndex)
		}
		if strings.TrimSpace(group.Label) == "" {
			return nil, fmt.Errorf("navigation group %q has empty label", group.Key)
		}
		if _, exists := groupKeys[group.Key]; exists {
			return nil, fmt.Errorf("duplicate navigation group key %q", group.Key)
		}
		groupKeys[group.Key] = struct{}{}
		for _, item := range group.Items {
			if err := validateItem(item, 1, itemKeys, knownPermissions); err != nil {
				return nil, err
			}
		}
	}

	return &Registry{groups: cloneGroups(groups)}, nil
}

func (registry *Registry) Prepare(currentPath string, can func(string) bool) []GroupView {
	groups := make([]GroupView, 0, len(registry.groups))
	for _, group := range registry.groups {
		items := make([]ItemView, 0, len(group.Items))
		for _, item := range group.Items {
			prepared, visible := prepareItem(item, 1, currentPath, can)
			if visible {
				items = append(items, prepared)
			}
		}
		if len(items) != 0 {
			groups = append(groups, GroupView{Key: group.Key, Label: group.Label, Items: items})
		}
	}
	return groups
}

func validateItem(item Item, depth int, itemKeys, knownPermissions map[string]struct{}) error {
	if depth > 3 {
		return fmt.Errorf("navigation item %q exceeds maximum depth 3", item.Key)
	}
	if strings.TrimSpace(item.Key) == "" {
		return fmt.Errorf("navigation item at depth %d has empty key", depth)
	}
	if _, exists := itemKeys[item.Key]; exists {
		return fmt.Errorf("duplicate navigation item key %q", item.Key)
	}
	itemKeys[item.Key] = struct{}{}
	if strings.TrimSpace(item.Label) == "" {
		return fmt.Errorf("navigation item %q has empty label", item.Key)
	}
	if item.Permission != "" && len(item.AnyPermissions) != 0 {
		return fmt.Errorf("navigation item %q cannot combine permission modes", item.Key)
	}
	if item.Permission != "" {
		if _, exists := knownPermissions[item.Permission]; !exists {
			return fmt.Errorf("navigation item %q has unknown permission %q", item.Key, item.Permission)
		}
	}
	for _, permission := range item.AnyPermissions {
		if _, exists := knownPermissions[permission]; !exists {
			return fmt.Errorf("navigation item %q has unknown permission %q", item.Key, permission)
		}
	}

	if len(item.Children) != 0 {
		if item.Path != "" || item.Match != "" {
			return fmt.Errorf("navigation container %q cannot have path or match mode", item.Key)
		}
		for _, child := range item.Children {
			if err := validateItem(child, depth+1, itemKeys, knownPermissions); err != nil {
				return err
			}
		}
		return nil
	}

	if item.Path == "" {
		return fmt.Errorf("navigation leaf %q has empty path", item.Key)
	}
	if item.Permission == "" && len(item.AnyPermissions) == 0 {
		return fmt.Errorf("navigation leaf %q has empty permission", item.Key)
	}
	if item.Match != MatchExact && item.Match != MatchPrefix {
		return fmt.Errorf("navigation leaf %q has invalid match mode %q", item.Key, item.Match)
	}
	parsed, err := url.ParseRequestURI(item.Path)
	if err != nil || !strings.HasPrefix(item.Path, "/") || strings.HasPrefix(item.Path, "//") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("navigation leaf %q has invalid path %q", item.Key, item.Path)
	}
	return nil
}

func prepareItem(item Item, depth int, currentPath string, can func(string) bool) (ItemView, bool) {
	if item.Permission != "" && !can(item.Permission) {
		return ItemView{}, false
	}
	if len(item.AnyPermissions) != 0 {
		visible := false
		for _, permission := range item.AnyPermissions {
			visible = visible || can(permission)
		}
		if !visible {
			return ItemView{}, false
		}
	}

	view := ItemView{Key: item.Key, Label: item.Label, Icon: item.Icon, Path: item.Path, Depth: depth}
	if len(item.Children) == 0 {
		view.Active = item.matches(currentPath)
		return view, true
	}

	view.Children = make([]ItemView, 0, len(item.Children))
	for _, child := range item.Children {
		prepared, visible := prepareItem(child, depth+1, currentPath, can)
		if visible {
			view.Children = append(view.Children, prepared)
			view.Active = view.Active || prepared.Active
		}
	}
	if len(view.Children) == 0 {
		return ItemView{}, false
	}
	view.Open = view.Active
	return view, true
}

func (item Item) matches(currentPath string) bool {
	if item.Match == MatchExact || item.Path == "/" {
		return currentPath == item.Path
	}
	base := strings.TrimSuffix(item.Path, "/")
	return currentPath == base || strings.HasPrefix(currentPath, base+"/")
}

func cloneGroups(groups []Group) []Group {
	cloned := make([]Group, len(groups))
	for index, group := range groups {
		cloned[index] = group
		cloned[index].Items = cloneItems(group.Items)
	}
	return cloned
}

func cloneItems(items []Item) []Item {
	cloned := make([]Item, len(items))
	for index, item := range items {
		cloned[index] = item
		cloned[index].AnyPermissions = append([]string(nil), item.AnyPermissions...)
		cloned[index].Children = cloneItems(item.Children)
	}
	return cloned
}
