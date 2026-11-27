package shared

const (
	// ContextUserIDKey is the Gin context key storing the authenticated user's ID (uint).
	ContextUserIDKey = "auth_user_id"
	// ContextEmailKey is the Gin context key storing the authenticated user's email (string).
	ContextEmailKey = "auth_user_email"
	// ContextRoleKey is the Gin context key storing the authenticated user's role (UserRole).
	ContextRoleKey = "auth_user_role"
)
