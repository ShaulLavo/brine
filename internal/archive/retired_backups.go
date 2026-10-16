package archive

import "context"

func (s Service) retireArchivedBackups(ctx context.Context, p Plan) error {
	host, ok := s.Host.(interface {
		RetireArchivedBackups(context.Context, Plan) error
	})
	if ok && host.RetireArchivedBackups(ctx, p) != nil {
		return ErrRecovery
	}
	return nil
}
