package db

import "time"

// Action values for hsi_config_history.action.
const (
	ActionUpsert = "upsert"
	ActionDelete = "delete"
)

// ConfigKey identifies a current-state row.
type ConfigKey struct {
	NodeUUID string
	UserID   string
}

// HSIConfigRow is a projected HSI config record. For current-state rows it is
// the latest value; for history rows Action records what happened. ConfigJSON
// is the full etcd value (config + metadata) and is nil for delete history.
type HSIConfigRow struct {
	NodeUUID        string
	UserID          string
	Action          string // history only: ActionUpsert | ActionDelete
	ConfigJSON      []byte
	DesireStatus    string
	ModRevision     int64
	ResourceVersion string
	UpdatedBy       string
	UpdatedAt       *time.Time
}
