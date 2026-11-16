package domain

import (
	"context"
	"log"

	"gorm.io/gorm"
)

// RecordServerLog persists a server lifecycle operation outcome (success or error) to PostgreSQL.
//
// Arguments:
//   - ctx: Operation context.
//   - db: Active GORM database handle.
//   - serverID: Target game server ID (0 if server row was not yet persisted).
//   - userID: Initiating user ID (0 if triggered internally).
//   - op: Operation enum value (create, stop, delete, backup, restore).
//   - status: Outcome enum value (success or error).
//   - message: Concise outcome summary or error detail.
//
// Example usage:
//
//	domain.RecordServerLog(ctx, db, srv.ID, srv.UserID, domain.ServerOpStop, domain.ServerLogStatusSuccess, "server stopped")
func RecordServerLog(ctx context.Context, db *gorm.DB, serverID, userID uint, op ServerOperationType, status ServerLogStatus, message string) {
	if db == nil {
		return
	}
	entry := ServerOperationLog{
		ServerID:  serverID,
		UserID:    userID,
		Operation: op,
		Status:    status,
		Message:   message,
	}
	if err := db.WithContext(ctx).Create(&entry).Error; err != nil {
		log.Printf("[WARN] failed recording server operation log (server=%d op=%s): %v", serverID, op, err)
	}
}

// ListServerLogs returns the most recent audited operation logs for a server.
//
// Arguments:
//   - ctx: Operation cancellation context.
//   - db: Active GORM database handle.
//   - serverID: Target game server ID.
//   - limit: Maximum rows to return (defaults to 25 when <= 0).
//
// Example usage:
//
//	logs, err := domain.ListServerLogs(ctx, db, 42, 20)
func ListServerLogs(ctx context.Context, db *gorm.DB, serverID uint, limit int) ([]ServerOperationLog, error) {
	if limit <= 0 {
		limit = 25
	}
	var logs []ServerOperationLog
	err := db.WithContext(ctx).
		Where("server_id = ?", serverID).
		Order("created_at desc, id desc").
		Limit(limit).
		Find(&logs).Error
	if err != nil {
		return nil, err
	}
	return logs, nil
}
