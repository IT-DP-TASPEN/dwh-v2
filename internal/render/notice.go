package render

type Notice struct {
	Severity string
	Title    string
	Message  string
}

var notices = map[string]Notice{
	"user-created":          {Severity: "success", Title: "User created", Message: "The user account is ready."},
	"user-updated":          {Severity: "success", Title: "User updated", Message: "Profile changes were saved."},
	"role-assigned":         {Severity: "success", Title: "Role assigned", Message: "The user's role was updated."},
	"user-activated":        {Severity: "success", Title: "User activated", Message: "The user can sign in again."},
	"user-deactivated":      {Severity: "success", Title: "User deactivated", Message: "The user and active impersonations were signed out."},
	"password-reset":        {Severity: "success", Title: "Password reset", Message: "The password was changed and owned sessions were revoked."},
	"role-created":          {Severity: "success", Title: "Role created", Message: "The custom role is ready."},
	"role-updated":          {Severity: "success", Title: "Role updated", Message: "Role changes were saved."},
	"role-deleted":          {Severity: "success", Title: "Role deleted", Message: "The unused custom role was removed."},
	"permissions-updated":   {Severity: "success", Title: "Permissions updated", Message: "Role permissions were replaced."},
	"impersonation-started": {Severity: "success", Title: "Impersonation started", Message: "Target permissions are now active."},
	"impersonation-stopped": {Severity: "success", Title: "Impersonation stopped", Message: "Administrator access is restored."},
	"registered":            {Severity: "success", Title: "Registration complete", Message: "You can now sign in."},
}

func NoticeFromID(id string) *Notice {
	notice, ok := notices[id]
	if !ok {
		return nil
	}
	return &notice
}
