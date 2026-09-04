package fincloudauth

import (
	"context"
	"time"

	"github.com/ibldzn/go-admin/internal/fincloud"
	"github.com/ibldzn/go-admin/internal/securityctx"
)

type Service struct {
	repository *Repository
	sessions   *fincloud.SessionCoordinator
}

func NewService(repository *Repository, sessions *fincloud.SessionCoordinator) *Service {
	return &Service{repository: repository, sessions: sessions}
}

func (service *Service) Test(ctx context.Context, requester securityctx.Requester, id uint64) error {
	return service.test(ctx, requester, id, 0)
}

func (service *Service) SetStatus(ctx context.Context, requester securityctx.Requester, id, expectedRevision uint64, status Status) error {
	if status == StatusActive {
		if err := service.test(ctx, requester, id, expectedRevision); err != nil {
			return err
		}
	}
	return service.repository.SetStatus(ctx, requester, id, expectedRevision, status, time.Now().UTC())
}

func (service *Service) test(ctx context.Context, requester securityctx.Requester, id, expectedRevision uint64) error {
	profile, err := service.repository.Find(ctx, id)
	if err != nil {
		return err
	}
	if profile.Status == StatusArchived {
		return ErrInactive
	}
	if expectedRevision != 0 && profile.Revision != expectedRevision {
		return ErrConflict
	}
	auth, err := service.repository.Auth(ctx, id, profile.Revision, false)
	if err == nil {
		err = service.sessions.Test(ctx, auth)
	}
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	if auditErr := service.repository.RecordTest(ctx, requester, id, profile.Revision, outcome, time.Now().UTC()); err == nil {
		err = auditErr
	}
	return err
}
