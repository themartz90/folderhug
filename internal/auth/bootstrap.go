package auth

import "context"

// SetupRequired only exposes setup after startup has prepared an empty installation.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	var required bool
	err := s.db.QueryRowContext(ctx, `SELECT
		NOT EXISTS(SELECT 1 FROM owner_auth) AND
		NOT EXISTS(SELECT 1 FROM settings WHERE key='owner_setup_complete') AND
		EXISTS(SELECT 1 FROM settings WHERE key='owner_setup_pending') AND
		EXISTS(SELECT 1 FROM settings WHERE key='credential_key_check')`).Scan(&required)
	return required, err
}
