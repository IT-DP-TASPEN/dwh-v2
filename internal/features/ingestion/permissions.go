package ingestion

import "github.com/ibldzn/go-admin/internal/access"

const (
	PermissionView             = "ingestion.view"
	PermissionRun              = "ingestion.run"
	PermissionRunAll           = "ingestion.run_all"
	PermissionCancel           = "ingestion.cancel"
	PermissionRecoverAbandoned = "ingestion.recover_abandoned"
)

func PermissionDefinitions() []access.PermissionDefinition {
	return []access.PermissionDefinition{
		{Key: PermissionView, Name: "View Ingestion", Group: "Ingestion", Description: "View ingestion operations and run history"},
		{Key: PermissionRun, Name: "Run Ingestion", Group: "Ingestion", Description: "Submit an individual ingestion job"},
		{Key: PermissionRunAll, Name: "Run All Ingestion", Group: "Ingestion", Description: "Submit all canonical ingestion jobs"},
		{Key: PermissionCancel, Name: "Cancel Ingestion", Group: "Ingestion", Description: "Request cancellation of an ingestion run"},
		{Key: PermissionRecoverAbandoned, Name: "Recover Abandoned Ingestion", Group: "Ingestion", Description: "Mark a run abandoned after verified worker loss"},
	}
}
