package repo

import (
	"crypto/ed25519"
	"io"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// Recipient is a public key that receives a wrap of every epoch (§4.1).
// Recovery recipients are created by Init; use the constructors for the
// other kinds.
type Recipient struct {
	Kind   vdcrypt.Kind
	Pub    vdcrypt.Key
	Label  string
	KDF    *vdcrypt.PassphraseKDF // passphrase only
	Device *vdcrypt.KeyFileDevice // device only
	Server *vdcrypt.KeyFileServer // escrow only
}

// DeviceRecipient is a device's X25519 box key plus its VD1 signing key.
func DeviceRecipient(boxPub vdcrypt.Key, deviceID int64, signPub ed25519.PublicKey, label string) Recipient {
	return Recipient{Kind: vdcrypt.KindDevice, Pub: boxPub, Label: label,
		Device: &vdcrypt.KeyFileDevice{ID: deviceID, SignPub: append([]byte(nil), signPub...)}}
}

// EscrowRecipient is the server escrow key SEK with its SIK signature
// (vdcrypt.SignSEK over sekPub || u32(version)).
func EscrowRecipient(sekPub vdcrypt.Key, sikPub ed25519.PublicKey, sekSig []byte, sekVersion uint32, label string) Recipient {
	return Recipient{Kind: vdcrypt.KindEscrow, Pub: sekPub, Label: label,
		Server: &vdcrypt.KeyFileServer{SIKPub: append([]byte(nil), sikPub...), SEKSig: append([]byte(nil), sekSig...), SEKVersion: sekVersion}}
}

// PassphraseRecipient derives the passphrase recipient (Argon2id). kdf nil
// means the writer defaults with a fresh salt. The passphrase must already
// be NFC-normalised UTF-8 (vdcrypt contract issue 4).
func PassphraseRecipient(rnd io.Reader, passphrase []byte, kdf *vdcrypt.PassphraseKDF, label string) (Recipient, error) {
	if kdf == nil {
		var err error
		if kdf, err = vdcrypt.NewPassphraseKDF(rnd); err != nil {
			return Recipient{}, err
		}
	}
	priv, pub, err := vdcrypt.PassphraseKeyPair(passphrase, kdf)
	if err != nil {
		return Recipient{}, err
	}
	priv.Wipe()
	return Recipient{Kind: vdcrypt.KindPassphrase, Pub: pub, Label: label, KDF: kdf}, nil
}

func recipientOf(kf *vdcrypt.KeyFile) Recipient {
	pub, _ := vdcrypt.KeyFromBytes(kf.RecipientPub)
	return Recipient{Kind: kf.Kind, Pub: pub, Label: kf.Label, KDF: kf.KDF, Device: kf.Device, Server: kf.Server}
}

// Credential is what Open unlocks a repository with.
type Credential struct {
	kind       vdcrypt.Kind
	priv       vdcrypt.Key // device, escrow
	secret     vdcrypt.Key // recovery
	passphrase []byte
}

// DeviceCredential opens with a device box private key.
func DeviceCredential(boxPriv vdcrypt.Key) Credential {
	return Credential{kind: vdcrypt.KindDevice, priv: boxPriv}
}

// EscrowCredential opens with the server escrow private key (SEK). Under
// escrow_mode=metadata the result is a metadata-only repository.
func EscrowCredential(sekPriv vdcrypt.Key) Credential {
	return Credential{kind: vdcrypt.KindEscrow, priv: sekPriv}
}

// RecoveryCredential opens with the VDRK1- recovery key text (§4.8).
func RecoveryCredential(text string) (Credential, error) {
	s, err := vdcrypt.DecodeRecoveryKey(text)
	if err != nil {
		return Credential{}, err
	}
	return Credential{kind: vdcrypt.KindRecovery, secret: s}, nil
}

// RecoverySecretCredential opens with the raw 32-byte recovery secret.
func RecoverySecretCredential(secret vdcrypt.Key) Credential {
	return Credential{kind: vdcrypt.KindRecovery, secret: secret}
}

// PassphraseCredential opens with a passphrase (NFC-normalised UTF-8).
func PassphraseCredential(passphrase []byte) Credential {
	return Credential{kind: vdcrypt.KindPassphrase, passphrase: append([]byte(nil), passphrase...)}
}

// Kind returns the recipient kind the credential unlocks.
func (c Credential) Kind() vdcrypt.Kind { return c.kind }

// Wipe clears the secrets held by c.
func (c *Credential) Wipe() {
	c.priv.Wipe()
	c.secret.Wipe()
	clear(c.passphrase)
}

// private returns the X25519 private key for a key-file recipient check;
// passphrase credentials have none (the key depends on each key file's KDF).
func (c Credential) private(repo vdcrypt.RepoID) (vdcrypt.Key, bool) {
	switch c.kind {
	case vdcrypt.KindDevice, vdcrypt.KindEscrow:
		return c.priv, true
	case vdcrypt.KindRecovery:
		return vdcrypt.RecoveryPrivate(repo, c.secret), true
	}
	return vdcrypt.Key{}, false
}
