package wire

// RepoFormatVersion is the only repository format version of api-v1 §5.
const RepoFormatVersion = 1

// Repo is a backup repository (api-v1 §5), returned by GET /repos (as
// List[Repo]) and GET /repos/{id} (dev, sess).
type Repo struct {
	ID                int64     `json:"id"`
	RepoUUID          string    `json:"repo_uuid"`
	Name              string    `json:"name"`
	DestinationID     int64     `json:"destination_id"`
	FormatVersion     int       `json:"format_version"`
	EscrowMode        string    `json:"escrow_mode"`
	CurrentEpoch      int64     `json:"current_epoch"`
	CreatedByDeviceID int64     `json:"created_by_device_id"`
	CreatedAt         int64     `json:"created_at"`
	Usage             RepoUsage `json:"usage"`
	LastCatalogSyncAt int64     `json:"last_catalog_sync_at"`
}

// RepoUsage is Repo.usage (api-v1 §5).
type RepoUsage struct {
	Objects      int64 `json:"objects"`
	StoredBytes  int64 `json:"stored_bytes"`
	LogicalBytes int64 `json:"logical_bytes"`
	Snapshots    int64 `json:"snapshots"`
}

// RepoCreateRequest is the body of POST /repos (dev; api-v1 §5). It is
// idempotent by RepoUUID; one repo per (user, destination). EscrowMode
// defaults to "full" when omitted.
type RepoCreateRequest struct {
	RepoUUID      string `json:"repo_uuid"`
	Name          string `json:"name"`
	DestinationID int64  `json:"destination_id"`
	EscrowMode    string `json:"escrow_mode,omitempty"`
	FormatVersion int    `json:"format_version"`
}

// Repository object kinds, the {kind} path segment of
// /repos/{id}/objects/{kind}/{oid} (api-v1 §5).
const (
	ObjectKindConfig    = "config"
	ObjectKindKeys      = "keys"
	ObjectKindChain     = "chain"
	ObjectKindIndex     = "index"
	ObjectKindSnapshots = "snapshots"
	ObjectKindData      = "data"
	ObjectKindLocks     = "locks"
)

// ObjectConfigOID is the fixed oid of the config object (api-v1 §5);
// writing it needs an exclusive lock.
const ObjectConfigOID = "0000000000000000000000000000000000000000000000000000000000000000"

// ObjectMissingMaxOIDs is the limit on ObjectMissingRequest.OIDs (api-v1 §5).
const ObjectMissingMaxOIDs = 20000

// ObjectInfo is one item of GET /repos/{id}/objects?kind=&cursor= (dev;
// api-v1 §5): an oid and its size (rev 1.0 §5), returned as
// List[ObjectInfo].
type ObjectInfo struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// ObjectMissingRequest is the body of POST /repos/{id}/objects/missing (dev;
// api-v1 §5).
type ObjectMissingRequest struct {
	Kind string   `json:"kind"`
	OIDs []string `json:"oids"`
}

// ObjectMissingResponse lists the requested oids the server does not hold;
// pending-delete objects count as missing (api-v1 §5).
type ObjectMissingResponse struct {
	Missing []string `json:"missing"`
}

// Repository lock kinds (api-v1 §5; backup-format lock objects).
const (
	LockKindShared    = "shared"
	LockKindExclusive = "exclusive"
)

// LockRequest is the body of POST /repos/{id}/locks (dev; api-v1 §5). A
// conflicting lock returns 409 repo_locked with LockedDetails.
// PUT /repos/{id}/locks/{lock_id} refreshes (extends the TTL) and
// DELETE releases; rev 1.0 §5 gives neither a body.
type LockRequest struct {
	Kind       string `json:"kind"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// Lock is the reply of acquiring a lock (rev 1.0 §5); LockID is an opaque
// string.
type Lock struct {
	LockID    string `json:"lock_id"`
	ExpiresAt int64  `json:"expires_at"`
}

// LockedDetails is ErrorBody.Details of 409 repo_locked (api-v1 §1.1).
type LockedDetails struct {
	Holder    string `json:"holder"`
	ExpiresAt int64  `json:"expires_at"`
}
