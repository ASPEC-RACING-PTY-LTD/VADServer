package wire

// Destination scopes (api-v1 §4).
const (
	DestinationScopeUser   = "user"
	DestinationScopeGlobal = "global"
)

// Destination kinds (api-v1 §4). Local covers server disks and NAS/SMB
// shares mounted on the Docker host.
const (
	DestinationKindServer = "server"
	DestinationKindLocal  = "local"
	DestinationKindS3     = "s3"
)

// S3 providers in DestinationConfig.Provider (api-v1 §4).
const (
	DestinationProviderAWS   = "aws"
	DestinationProviderR2    = "r2"
	DestinationProviderB2    = "b2"
	DestinationProviderMinIO = "minio"
	DestinationProviderOther = "other"
)

// Destination health values (api-v1 §4).
const (
	DestinationHealthOK          = "ok"
	DestinationHealthDegraded    = "degraded"
	DestinationHealthUnreachable = "unreachable"
	DestinationHealthUnknown     = "unknown"
)

// Destination is a server-side storage destination (api-v1 §4), returned by
// GET /destinations[/{id}] and /admin/destinations[/{id}]. The credential is
// never returned; the embedded SecretState shows whether one is stored.
// Devices never receive destinations in this form (see DestinationRef).
type Destination struct {
	ID     int64             `json:"id"`
	Scope  string            `json:"scope"`
	Name   string            `json:"name"`
	Kind   string            `json:"kind"`
	Config DestinationConfig `json:"config"`
	SecretState
	Enabled       bool                    `json:"enabled"`
	Health        string                  `json:"health"`
	LastCheckedAt int64                   `json:"last_checked_at"`
	Capabilities  DestinationCapabilities `json:"capabilities"`
	CreatedAt     int64                   `json:"created_at"`
	UpdatedAt     int64                   `json:"updated_at"`
}

// DestinationConfig is the non-secret configuration of a destination
// (api-v1 §4). Which fields apply depends on the kind (Path for local, the
// rest for s3); rev 1.0 §4 lists every field, so unused ones are sent empty.
type DestinationConfig struct {
	Path      string `json:"path"`
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	Region    string `json:"region"`
	Provider  string `json:"provider"`
	PathStyle bool   `json:"path_style"`
}

// DestinationCapabilities reports what the backing store supports
// (api-v1 §4).
type DestinationCapabilities struct {
	ObjectLock     bool `json:"object_lock"`
	ConditionalPut bool `json:"conditional_put"`
	ServerSideCopy bool `json:"server_side_copy"`
}

// DestinationRef is the device view of GET /destinations: names and ids only
// (api-v1 §4).
type DestinationRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// DestinationCreateRequest is the body of POST /destinations and
// POST /admin/destinations (api-v1 §4). The scope follows from the route.
// Secret is write-only.
type DestinationCreateRequest struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	Config  DestinationConfig `json:"config"`
	Secret  SecretUpdate      `json:"secret,omitzero"`
	Enabled *bool             `json:"enabled,omitempty"`
}

// DestinationUpdateRequest is the body of PATCH /destinations/{id} and
// /admin/destinations/{id} (api-v1 §4). Omitted fields are kept; Config,
// when present, replaces the whole config.
type DestinationUpdateRequest struct {
	Name    *string            `json:"name,omitempty"`
	Config  *DestinationConfig `json:"config,omitempty"`
	Secret  SecretUpdate       `json:"secret,omitzero"`
	Enabled *bool              `json:"enabled,omitempty"`
}

// DestinationTestRequest is the body of POST /destinations/test, which checks
// an unsaved destination (api-v1 §4). POST /destinations/{id}/test has no
// body.
type DestinationTestRequest struct {
	Kind   string            `json:"kind"`
	Config DestinationConfig `json:"config"`
	Secret *string           `json:"secret,omitempty"`
}

// DestinationTestResult is the reply of both test routes (rev 1.0 §4).
type DestinationTestResult struct {
	OK     bool                   `json:"ok"`
	Checks []DestinationTestCheck `json:"checks"`
}

// DestinationTestCheck is one check of a destination test (rev 1.0 §4).
type DestinationTestCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// DestinationProvider is one entry of GET /destinations/providers, which
// returns a bare JSON array: the form schema of one kind/provider pair
// (rev 1.0 §4).
type DestinationProvider struct {
	Kind     string                     `json:"kind"`
	Provider string                     `json:"provider"`
	Fields   []DestinationProviderField `json:"fields"`
}

// DestinationProviderField is one form field of a DestinationProvider
// (rev 1.0 §4). Secret marks the write-only credential field.
type DestinationProviderField struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Secret   bool   `json:"secret"`
}
