package customdatasets

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView   = "custom_datasets.view"
	PermissionManage = "custom_datasets.manage"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Custom Datasets", Group: "Custom Datasets", Description: "View custom datasets, schemas, imports, samples, and SQL access names"},
		{Key: PermissionManage, Name: "Manage Custom Datasets", Group: "Custom Datasets", Description: "Upload, publish, edit, and archive custom datasets"},
	}
}
