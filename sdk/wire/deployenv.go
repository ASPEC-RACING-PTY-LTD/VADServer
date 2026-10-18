package wire

import "fmt"

// DeployEnvAdded is a variable added by an upgrade.
type DeployEnvAdded struct {
	Name    string `json:"name"`
	Default string `json:"default"`
	Since   string `json:"since"`
}

// DeployEnvObsolete is an obsolete variable still present in the host .env.
type DeployEnvObsolete struct {
	Name            string `json:"name"`
	DeprecatedSince string `json:"deprecated_since"`
	Replacement     string `json:"replacement"`
	Note            string `json:"note"`
}

// DeployEnvRemovalApproval is an approved removal waiting for
// install.sh upgrade.
type DeployEnvRemovalApproval struct {
	Name       string `json:"name"`
	ApprovedBy int64  `json:"approved_by"`
	ApprovedAt int64  `json:"approved_at"`
}

// DeployEnvStatus is the reply of GET /admin/deploy/env (super). Values of
// secret variables are never returned.
type DeployEnvStatus struct {
	SchemaVersion   int                        `json:"schema_version"`
	AddedByUpgrade  []DeployEnvAdded           `json:"added_by_upgrade"`
	ObsoletePresent []DeployEnvObsolete        `json:"obsolete_present"`
	PendingRemovals []DeployEnvRemovalApproval `json:"pending_removals"`
}

// DeployEnvRemovalRequest is the body of POST /admin/deploy/env/removals
// (super). ConfirmText must equal DeployEnvRemovalConfirmText(len(Names)).
type DeployEnvRemovalRequest struct {
	Names       []string `json:"names"`
	ConfirmText string   `json:"confirm_text"`
}

// DeployEnvRemovalConfirmText returns the required confirm_text for n names.
func DeployEnvRemovalConfirmText(n int) string {
	return fmt.Sprintf("REMOVE %d VARIABLES", n)
}
