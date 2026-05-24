package models

type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

const ErrUnknownRole = "unknown role"

func (r Role) IsValid() bool {
	switch r {
	case RoleAdmin, RoleUser:
		return true
	default:
		return false
	}
}

func (r Role) IsAdmin() bool {
	return r == RoleAdmin
}
