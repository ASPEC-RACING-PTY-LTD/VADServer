package wire

// SuperuserUpdateRequest is the body of PATCH /admin/users/{id} (super) that
// grants or revokes is_superuser. Revoking the last superuser returns 409.
// POST /admin/keys/rotate-server (super) has no body and replies
// OperationAccepted.
type SuperuserUpdateRequest struct {
	IsSuperuser bool `json:"is_superuser"`
}
