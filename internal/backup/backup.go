package backup

// Service coordinates world data backup snapshots and restores against remote object storage.
type Service struct{}

// NewService instantiates a backup and disaster recovery service.
//
// Arguments:
//   - None.
//
// Example usage:
//   svc := backup.NewService()
func NewService() *Service {
	return &Service{}
}
