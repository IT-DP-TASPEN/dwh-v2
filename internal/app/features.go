package app

import (
	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"time"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/coordinator"
	"github.com/ibldzn/go-admin/internal/features/auditlogs"
	"github.com/ibldzn/go-admin/internal/features/dashboard"
	"github.com/ibldzn/go-admin/internal/features/datasources"
	"github.com/ibldzn/go-admin/internal/features/impersonation"
	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/features/reports"
	"github.com/ibldzn/go-admin/internal/features/reporttemplates"
	"github.com/ibldzn/go-admin/internal/features/roles"
	schedulesfeature "github.com/ibldzn/go-admin/internal/features/schedules"
	sourcesfeature "github.com/ibldzn/go-admin/internal/features/sources"
	"github.com/ibldzn/go-admin/internal/features/users"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/reportexport"
	"github.com/ibldzn/go-admin/internal/reporting"
	"github.com/ibldzn/go-admin/internal/scheduler"
	"github.com/ibldzn/go-admin/internal/user"
)

func PermissionDefinitions() []access.PermissionDefinition {
	definitions := make([]access.PermissionDefinition, 0, 38)
	definitions = append(definitions, users.PermissionDefinitions()...)
	definitions = append(definitions, roles.PermissionDefinitions()...)
	definitions = append(definitions, auditlogs.PermissionDefinitions()...)
	definitions = append(definitions, ingestionfeature.PermissionDefinitions()...)
	definitions = append(definitions, sourcesfeature.PermissionDefinitions()...)
	definitions = append(definitions, schedulesfeature.PermissionDefinitions()...)
	definitions = append(definitions, datasources.PermissionDefinitions()...)
	definitions = append(definitions, reporttemplates.PermissionDefinitions()...)
	definitions = append(definitions, reports.PermissionDefinitions()...)
	return definitions
}

type featureDependencies struct {
	database            *sqlx.DB
	users               *user.Repository
	access              *access.Repository
	admin               *adminshell.Shell
	cookies             browserauth.CookieManager
	coordinator         *coordinator.Coordinator
	scheduler           *scheduler.Service
	reportingRepository *reporting.Repository
	reportingService    *reporting.Service
	reportingPools      *reporting.PoolManager
	exportRepository    *reportexport.Repository
	exportStorage       *reportexport.Storage
	downloadTimeout     time.Duration
}

func registerFeatureRoutes(router chi.Router, dependencies featureDependencies) {
	userService := users.NewService(users.NewRepository(dependencies.database, audit.Append), dependencies.access, roles.PermissionAssign)
	roleService := roles.NewService(roles.NewRepository(dependencies.database, audit.Append), PermissionDefinitions())
	impersonationService := impersonation.NewService(
		dependencies.users,
		dependencies.access,
		impersonation.NewRepository(dependencies.database, audit.Append),
	)
	auditLogService := auditlogs.NewService(auditlogs.NewRepository(dependencies.database))
	ingestionService, err := ingestionfeature.NewService(ingestionfeature.NewRepository(dependencies.database))
	if err != nil {
		panic(err)
	}
	sourceService, err := sourcesfeature.NewService(dependencies.database)
	if err != nil {
		panic(err)
	}
	scheduleService, err := schedulesfeature.NewService(dependencies.database, dependencies.scheduler)
	if err != nil {
		panic(err)
	}

	datasourceHandler := datasources.NewHandler(dependencies.admin, dependencies.reportingRepository, dependencies.reportingService, dependencies.reportingPools)
	templateHandler := reporttemplates.NewHandler(dependencies.admin, dependencies.reportingRepository, dependencies.reportingService)
	reportHandler := reports.NewHandler(dependencies.admin, dependencies.reportingRepository, dependencies.reportingService, dependencies.exportRepository, dependencies.exportStorage, dependencies.downloadTimeout)

	dashboard.NewHandler(dependencies.admin, dashboard.NewService(ingestionService, dependencies.exportRepository)).RegisterRoutes(router)
	users.NewHandler(dependencies.admin, userService, dependencies.cookies, roles.PermissionAssign, impersonation.CanStart).RegisterRoutes(router)
	roles.NewHandler(dependencies.admin, roleService).RegisterRoutes(router)
	impersonation.NewHandler(dependencies.admin, impersonationService, dependencies.cookies).RegisterRoutes(router)
	auditlogs.NewHandler(dependencies.admin, auditLogService).RegisterRoutes(router)
	ingestionfeature.NewHandler(dependencies.admin, ingestionService, dependencies.coordinator).RegisterRoutes(router)
	sourcesfeature.NewHandler(dependencies.admin, sourceService, dependencies.coordinator).RegisterRoutes(router)
	schedulesfeature.NewHandler(dependencies.admin, scheduleService).RegisterRoutes(router)
	datasourceHandler.RegisterRoutes(router)
	templateHandler.RegisterRoutes(router)
	reportHandler.RegisterRoutes(router)
}

func navigationGroups() []navigation.Group {
	return []navigation.Group{
		{Key: "general", Label: "General", Items: []navigation.Item{dashboard.Navigation()}},
		{Key: "data-ingestion", Label: "Data Ingestion", Items: []navigation.Item{
			ingestionfeature.OverviewNavigation(), sourcesfeature.Navigation(), ingestionfeature.RunsNavigation(), schedulesfeature.Navigation(),
		}},
		{Key: "reporting", Label: "Reporting", Items: []navigation.Item{
			reports.Navigation(), reports.ExportsNavigation(),
			{Key: "report-configuration", Label: "Configuration", Icon: "settings", Children: []navigation.Item{reporttemplates.Navigation(), datasources.Navigation()}},
		}},
		{Key: "management", Label: "Management", Items: []navigation.Item{
			users.Navigation(),
			{Key: "access-control", Label: "Access Control", Icon: "shield", Children: []navigation.Item{roles.Navigation()}},
		}},
		{Key: "system", Label: "System", Items: []navigation.Item{auditlogs.Navigation()}},
	}
}
