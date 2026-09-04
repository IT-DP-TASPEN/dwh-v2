package render

type Notice struct {
	Severity string
	Title    string
	Message  string
}

var notices = map[string]Notice{
	"user-created":                      {Severity: "success", Title: "User created", Message: "The user account is ready."},
	"user-updated":                      {Severity: "success", Title: "User updated", Message: "Profile changes were saved."},
	"role-assigned":                     {Severity: "success", Title: "Role assigned", Message: "The user's role was updated."},
	"user-activated":                    {Severity: "success", Title: "User activated", Message: "The user can sign in again."},
	"user-deactivated":                  {Severity: "success", Title: "User deactivated", Message: "The user and active impersonations were signed out."},
	"password-reset":                    {Severity: "success", Title: "Password reset", Message: "The password was changed and owned sessions were revoked."},
	"role-created":                      {Severity: "success", Title: "Role created", Message: "The custom role is ready."},
	"role-updated":                      {Severity: "success", Title: "Role updated", Message: "Role changes were saved."},
	"role-deleted":                      {Severity: "success", Title: "Role deleted", Message: "The unused custom role was removed."},
	"permissions-updated":               {Severity: "success", Title: "Permissions updated", Message: "Role permissions were replaced."},
	"impersonation-started":             {Severity: "success", Title: "Impersonation started", Message: "Target permissions are now active."},
	"impersonation-stopped":             {Severity: "success", Title: "Impersonation stopped", Message: "Administrator access is restored."},
	"registered":                        {Severity: "success", Title: "Registration complete", Message: "You can now sign in."},
	"source-updated":                    {Severity: "success", Title: "Source updated", Message: "The source setting was saved."},
	"source-auth-profile-updated":       {Severity: "success", Title: "Source authentication updated", Message: "The source Auth Profile assignment was saved."},
	"fincloud-auth-profile-created":     {Severity: "success", Title: "Auth Profile created", Message: "The disabled Fincloud Auth Profile was saved."},
	"fincloud-auth-profile-updated":     {Severity: "success", Title: "Auth Profile updated", Message: "The Fincloud authentication settings were saved."},
	"fincloud-auth-profile-state":       {Severity: "success", Title: "Auth Profile state updated", Message: "The Fincloud Auth Profile state was saved."},
	"fincloud-auth-profile-test-ok":     {Severity: "success", Title: "Connection succeeded", Message: "Fincloud accepted the saved username, password, role, and location."},
	"fincloud-auth-profile-test-failed": {Severity: "warning", Title: "Connection failed", Message: "Fincloud did not accept the saved authentication settings."},
	"schedule-created":                  {Severity: "success", Title: "Schedule created", Message: "The schedule is ready."},
	"schedule-updated":                  {Severity: "success", Title: "Schedule updated", Message: "The schedule state was saved."},
	"cancellation-requested":            {Severity: "success", Title: "Cancellation requested", Message: "The worker will stop at the next safe boundary."},
	"run-abandoned":                     {Severity: "warning", Title: "Run marked abandoned", Message: "Worker ownership was recorded as irrecoverably lost."},
	"report-datasource-created":         {Severity: "success", Title: "Datasource created", Message: "The disabled datasource draft was saved."},
	"report-datasource-updated":         {Severity: "success", Title: "Datasource updated", Message: "Connection settings were saved."},
	"report-datasource-state":           {Severity: "success", Title: "Datasource state updated", Message: "The datasource state was saved."},
	"report-datasource-test-ok":         {Severity: "success", Title: "Connection succeeded", Message: "The reporting datasource accepted a verified connection."},
	"report-datasource-test-failed":     {Severity: "warning", Title: "Connection failed", Message: "The datasource could not be reached with the saved settings."},
	"report-export-submitted":           {Severity: "success", Title: "Export queued", Message: "The full report will be generated in the background."},
}

func NoticeFromID(id string) *Notice {
	notice, ok := notices[id]
	if !ok {
		return nil
	}
	return &notice
}
