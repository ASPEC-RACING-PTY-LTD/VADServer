package wire

// DirectProbe is the reply of POST /devices/direct-probe (upload.direct,
// dev): a one-time nonce whose Answer only this server knows. The client
// asks GET /devices/direct-probe/{nonce} at the local address and compares.
// ResetGen rises when an administrator resets the device's direct uploads.
type DirectProbe struct {
	Nonce     string `json:"nonce"`
	Answer    string `json:"answer"`
	ResetGen  int64  `json:"reset_gen"`
	ExpiresIn int64  `json:"expires_in"`
}

// DirectProbeAnswer is the reply of GET /devices/direct-probe/{nonce}.
type DirectProbeAnswer struct {
	Answer string `json:"answer"`
}
