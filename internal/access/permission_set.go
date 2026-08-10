package access

type PermissionSet struct {
	keys map[string]struct{}
}

func NewPermissionSet(keys []string) PermissionSet {
	set := PermissionSet{keys: make(map[string]struct{}, len(keys))}
	for _, key := range keys {
		set.keys[key] = struct{}{}
	}
	return set
}

func (set PermissionSet) Has(key string) bool {
	_, ok := set.keys[key]
	return ok
}

func IsAdminRole(slug string) bool {
	return slug == AdminRoleSlug
}
