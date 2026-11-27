package shared

// UserRole represents platform permission levels.
type UserRole string

const (
	UserRoleUser  UserRole = "user"
	UserRoleStaff UserRole = "staff"
	UserRoleAdmin UserRole = "admin"
)

// IsStaffOrAdmin reports whether the role grants staff-level or administrator privileges.
//
// Arguments:
//   - None (method receiver is the UserRole value).
//
// Example usage:
//
//	if role.IsStaffOrAdmin() { ... }
func (r UserRole) IsStaffOrAdmin() bool {
	return r == UserRoleStaff || r == UserRoleAdmin
}

// IsAdmin reports whether the role grants administrator privileges.
//
// Arguments:
//   - None (method receiver is the UserRole value).
//
// Example usage:
//
//	if role.IsAdmin() { ... }
func (r UserRole) IsAdmin() bool {
	return r == UserRoleAdmin
}

// IsValid reports whether the role is one of the supported platform roles.
//
// Arguments:
//   - None (method receiver is the UserRole value).
//
// Example usage:
//
//	if !role.IsValid() { ... }
func (r UserRole) IsValid() bool {
	return r == UserRoleUser || r == UserRoleStaff || r == UserRoleAdmin
}
