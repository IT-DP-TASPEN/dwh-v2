package audit

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestStableActionsAreUnique(t *testing.T) {
	actions := []Action{
		ActionAuthLogin, ActionAuthLogout, ActionAuthRegistration,
		ActionImpersonationStarted, ActionImpersonationStopped,
		ActionUserCreated, ActionUserProfileUpdated, ActionUserRoleChanged,
		ActionUserActivated, ActionUserDeactivated, ActionUserPasswordReset,
		ActionRoleCreated, ActionRoleUpdated, ActionRoleDeleted, ActionRolePermissionsUpdated,
		ActionAdminBootstrap,
		ActionReportDatasourceCreated, ActionReportDatasourceUpdated, ActionReportDatasourceStateChanged, ActionReportDatasourceTested,
		ActionReportTemplateCreated, ActionReportTemplateUpdated, ActionReportTemplateStateChanged, ActionReportTemplateAccessChanged,
		ActionReportExecuted, ActionReportExportSubmitted, ActionReportExportDownloaded,
	}
	seen := make(map[Action]bool, len(actions))
	for _, action := range actions {
		if !knownAction(action) || seen[action] || action != Action(strings.ToLower(string(action))) {
			t.Fatalf("invalid or duplicate stable action %q", action)
		}
		seen[action] = true
	}
	if knownAction("made.up") {
		t.Fatal("unknown action accepted")
	}
}

func TestMetadataIsTypedAndSecretFree(t *testing.T) {
	metadata := []Metadata{
		RoleChangeMetadata{FromRole: "user", ToRole: "manager"},
		StatusChangeMetadata{From: "active", To: "inactive"},
		PermissionsUpdatedMetadata{Added: []string{"users.view"}, Removed: []string{"roles.view"}},
		ImpersonationStartedMetadata{TargetRole: "manager"},
	}
	for _, value := range metadata {
		typeOf := reflect.TypeOf(value)
		for index := 0; index < typeOf.NumField(); index++ {
			field := strings.ToLower(typeOf.Field(index).Name + " " + typeOf.Field(index).Tag.Get("json"))
			for _, forbidden := range []string{"password", "token", "cookie", "authorization", "credential", "hash"} {
				if strings.Contains(field, forbidden) {
					t.Fatalf("metadata field %s contains forbidden secret category %q", typeOf.Field(index).Name, forbidden)
				}
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil || !json.Valid(encoded) {
			t.Fatalf("metadata %T did not encode safely: %s %v", value, encoded, err)
		}
	}
}

func TestIdentityValidation(t *testing.T) {
	if err := validateIdentity("actor", nil); err != nil {
		t.Fatal(err)
	}
	if err := validateIdentity("actor", &Identity{UserID: 1, Username: "admin"}); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []*Identity{{Username: "admin"}, {UserID: 1}, {UserID: 1, Username: " "}} {
		if err := validateIdentity("actor", identity); err == nil {
			t.Fatalf("invalid identity accepted: %+v", identity)
		}
	}
}
