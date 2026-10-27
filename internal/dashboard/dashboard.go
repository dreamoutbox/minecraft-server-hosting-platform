package dashboard

// Service provides view models and data feeds for the user management dashboard.
type Service struct{}

// NewService instantiates a user management dashboard service.
//
// Arguments:
//   - None.
//
// Example usage:
//   svc := dashboard.NewService()
func NewService() *Service {
	return &Service{}
}
