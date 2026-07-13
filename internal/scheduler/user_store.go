package scheduler

import (
	"database/sql"
	"fmt"

	"github.com/JBraunsmaJr/forge/internal/api"
)

func (s *Store) GetOrCreateUserBySSO(provider, externalID, email, name string) (*api.UserInfo, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var userID string
	err = tx.QueryRow(`
		SELECT user_id FROM sso_identities WHERE provider = $1 AND external_id = $2`,
		provider, externalID).Scan(&userID)

	if err == sql.ErrNoRows {
		// Try to find user by email first to link accounts
		err = tx.QueryRow(`SELECT id FROM users WHERE email = $1`, email).Scan(&userID)
		if err == sql.ErrNoRows {
			userID = newID()
			_, err = tx.Exec(`
				INSERT INTO users (id, email, name, role)
				VALUES ($1, $2, $3, 'viewer')`,
				userID, email, name)
			if err != nil {
				return nil, fmt.Errorf("create user: %w", err)
			}
		}

		_, err = tx.Exec(`
			INSERT INTO sso_identities (id, user_id, provider, external_id)
			VALUES ($1, $2, $3, $4)`,
			newID(), userID, provider, externalID)
		if err != nil {
			return nil, fmt.Errorf("create sso identity: %w", err)
		}
	} else if err != nil {
		return nil, err
	}

	_, err = tx.Exec(`UPDATE sso_identities SET last_login = NOW() WHERE user_id = $1 AND provider = $2`, userID, provider)
	if err != nil {
		return nil, err
	}

	var u api.UserInfo
	err = tx.QueryRow(`SELECT id, email, name, role, created_at FROM users WHERE id = $1`, userID).Scan(
		&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}

	return &u, tx.Commit()
}

func (s *Store) GetUserByID(id string) (*api.UserInfo, error) {
	var u api.UserInfo
	err := s.db.QueryRow(`SELECT id, email, name, role, created_at FROM users WHERE id = $1`, id).Scan(
		&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
