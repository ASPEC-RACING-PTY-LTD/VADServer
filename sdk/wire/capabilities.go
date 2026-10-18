package wire

// Repository formats advertised in Capabilities.Formats (api-v1 §2).
const CapabilitiesFormatRepoV1 = "vaultdock.repo/1"

// Escrow modes of a repository (api-v1 §2, §5A, §5).
const (
	EscrowModeFull     = "full"
	EscrowModeMetadata = "metadata"
)

// Capabilities is the reply of GET /backup/capabilities (dev, sess;
// api-v1 §2). ModulesSupported holds module ids from api-v1 §6A.
type Capabilities struct {
	Formats           []string `json:"formats"`
	MaxObjectBytes    int64    `json:"max_object_bytes"`
	PackTargetBytes   int64    `json:"pack_target_bytes"`
	MaxMissingBatch   int      `json:"max_missing_batch"`
	MaxCommitBytes    int64    `json:"max_commit_bytes"`
	EscrowModes       []string `json:"escrow_modes"`
	DefaultEscrowMode string   `json:"default_escrow_mode"`
	LongPollSeconds   int      `json:"long_poll_seconds"`
	ModulesSupported  []string `json:"modules_supported"`
}
