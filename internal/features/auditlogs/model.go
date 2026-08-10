package auditlogs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ibldzn/go-admin/internal/audit"
	"github.com/ibldzn/go-admin/internal/platform/pagination"
)

const PageSize = 50

type Record struct {
	ID                uint64
	ActorUserID       *uint64
	ActorUsername     string
	EffectiveUserID   *uint64
	EffectiveUsername string
	Action            string
	ResourceType      string
	ResourceID        *uint64
	Metadata          []byte
	CreatedAt         time.Time
}

type Page struct {
	Rows       []Record
	Action     string
	Pagination pagination.Page
}

type IdentityView struct {
	Label    string
	UserID   *uint64
	Username string
}

func (record Record) Actor() IdentityView {
	return identityView(record.Action, true, record.ActorUserID, record.ActorUsername)
}

func (record Record) Effective() IdentityView {
	return identityView(record.Action, false, record.EffectiveUserID, record.EffectiveUsername)
}

func (record Record) IsImpersonated() bool {
	if record.ActorUserID != nil && record.EffectiveUserID != nil {
		return *record.ActorUserID != *record.EffectiveUserID
	}
	return record.ActorUsername != "" && record.EffectiveUsername != "" && record.ActorUsername != record.EffectiveUsername
}

func (record Record) ResourceLabel() string {
	if record.ResourceType == "" || record.ResourceID == nil {
		return "—"
	}
	return fmt.Sprintf("%s #%d", record.ResourceType, *record.ResourceID)
}

func (record Record) MetadataText() string {
	if len(record.Metadata) == 0 {
		return "—"
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, record.Metadata, "", "  ") == nil {
		return pretty.String()
	}
	return string(record.Metadata)
}

func identityView(action string, actor bool, id *uint64, username string) IdentityView {
	if username != "" {
		return IdentityView{Label: "@" + username, UserID: id, Username: username}
	}
	if actor {
		switch audit.Action(action) {
		case audit.ActionAuthRegistration:
			return IdentityView{Label: "Public"}
		case audit.ActionAdminBootstrap:
			return IdentityView{Label: "System"}
		}
	}
	return IdentityView{Label: "—", UserID: id}
}
