package app

import (
	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/ibldzn/go-admin/internal/access"
	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/browserauth"
	"github.com/ibldzn/go-admin/internal/coordinator"
	"github.com/ibldzn/go-admin/internal/features/auditlogs"
	"github.com/ibldzn/go-admin/internal/features/dashboard"
	"github.com/ibldzn/go-admin/internal/features/impersonation"
	ingestionfeature "github.com/ibldzn/go-admin/internal/features/ingestion"
	"github.com/ibldzn/go-admin/internal/features/roles"
	schedulesfeature "github.com/ibldzn/go-admin/internal/features/schedules"
	sourcesfeature "github.com/ibldzn/go-admin/internal/features/sources"
	"github.com/ibldzn/go-admin/internal/features/users"
	"github.com/ibldzn/go-admin/internal/platform/adminshell"
	"github.com/ibldzn/go-admin/internal/platform/navigation"
	"github.com/ibldzn/go-admin/internal/scheduler"
	"github.com/ibldzn/go-admin/internal/user"
)

func PermissionDefinitions() []access.PermissionDefinition {
	definitions := make([]access.PermissionDefinition, 0, 25)
	definitions = append(definitions, dashboard.PermissionDefinitions()...)
	definitions = append(definitions, users.PermissionDefinitions()...)
	definitions = append(definitions, roles.PermissionDefinitions()...)
	definitions = append(definitions, auditlogs.PermissionDefinitions()...)
	definitions = append(definitions, ingestionfeature.PermissionDefinitions()...)
	definitions = append(definitions, sourcesfeature.PermissionDefinitions()...)
	definitions = append(definitions, schedulesfeature.PermissionDefinitions()...)
	return definitions
}

type featureDependencies struct {
	database    *sqlx.DB
	users       *user.Repository
	access      *access.Repository
	admin       *adminshell.Shell
	cookies     browserauth.CookieManager
	coordinator *coordinator.Coordinator
	scheduler   *scheduler.Service
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

	dashboard.NewHandler(dependencies.admin).RegisterRoutes(router)
	users.NewHandler(dependencies.admin, userService, dependencies.cookies, roles.PermissionAssign, impersonation.CanStart).RegisterRoutes(router)
	roles.NewHandler(dependencies.admin, roleService).RegisterRoutes(router)
	impersonation.NewHandler(dependencies.admin, impersonationService, dependencies.cookies).RegisterRoutes(router)
	auditlogs.NewHandler(dependencies.admin, auditLogService).RegisterRoutes(router)
	ingestionfeature.NewHandler(dependencies.admin, ingestionService, dependencies.coordinator).RegisterRoutes(router)
	sourcesfeature.NewHandler(dependencies.admin, sourceService, dependencies.coordinator).RegisterRoutes(router)
	schedulesfeature.NewHandler(dependencies.admin, scheduleService).RegisterRoutes(router)
}

func navigationGroups() []navigation.Group {
	return []navigation.Group{
		{Key: "general", Label: "General", Items: []navigation.Item{dashboard.Navigation()}},
		{Key: "data-ingestion", Label: "Data Ingestion", Items: []navigation.Item{
			ingestionfeature.OverviewNavigation(), sourcesfeature.Navigation(), ingestionfeature.RunsNavigation(), schedulesfeature.Navigation(),
		}},
		{Key: "management", Label: "Management", Items: []navigation.Item{
			users.Navigation(),
			{Key: "access-control", Label: "Access Control", Icon: "shield", Children: []navigation.Item{roles.Navigation()}},
		}},
		{Key: "system", Label: "System", Items: []navigation.Item{auditlogs.Navigation()}},
	}
}
