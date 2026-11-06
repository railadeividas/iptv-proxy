package store

import (
	"errors"
	"strings"
	"time"
)

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Password string `json:"-"` // hash
	Role     string `json:"role"`
	Created  int64  `json:"created_at"`
	Profiles int    `json:"profiles"` // how many profiles the user owns
}

// ErrExists is returned when a user name is taken.
var ErrExists = errors.New("that user name is already taken")

const userCols = `id,username,password,role,created_at,(SELECT COUNT(*) FROM profiles p WHERE p.owner_id=users.id)`

func (s *Store) CountUsers() int {
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.DB.Query(`SELECT ` + userCols + ` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Password, &u.Role, &u.Created, &u.Profiles); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) GetUser(id int64) (*User, error) {
	var u User
	err := s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id).Scan(&u.ID, &u.Username, &u.Password, &u.Role, &u.Created, &u.Profiles)
	return &u, err
}

func (s *Store) UserByName(name string) (*User, error) {
	var u User
	err := s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, name).Scan(&u.ID, &u.Username, &u.Password, &u.Role, &u.Created, &u.Profiles)
	return &u, err
}

// CreateUser adds a user; hash is an auth.Hash result.
func (s *Store) CreateUser(name, hash, role string) (*User, error) {
	res, err := s.DB.Exec(`INSERT INTO users(username,password,role,created_at) VALUES(?,?,?,?)`, name, hash, role, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetUser(id)
}

// CreateFirstUser adds the superuser, but only while there are no users at all (two first-start
// requests racing cannot both win).
func (s *Store) CreateFirstUser(name, hash, role string) (*User, error) {
	res, err := s.DB.Exec(`INSERT INTO users(username,password,role,created_at) SELECT ?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		name, hash, role, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("setup is already done")
	}
	id, _ := res.LastInsertId()
	return s.GetUser(id)
}

// UpdateUser changes the fields that are not empty (hash is a new password hash).
func (s *Store) UpdateUser(id int64, name, hash, role string) error {
	if name != "" {
		if _, err := s.DB.Exec(`UPDATE users SET username=? WHERE id=?`, name, id); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrExists
			}
			return err
		}
	}
	if hash != "" {
		if _, err := s.DB.Exec(`UPDATE users SET password=? WHERE id=?`, hash, id); err != nil {
			return err
		}
	}
	if role != "" {
		if _, err := s.DB.Exec(`UPDATE users SET role=? WHERE id=?`, role, id); err != nil {
			return err
		}
	}
	s.notify("users")
	return nil
}

// DeleteUser removes a user together with their profiles.
func (s *Store) DeleteUser(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM users WHERE id=?`, id)
	s.notify("users")
	s.notify("profiles")
	return err
}
