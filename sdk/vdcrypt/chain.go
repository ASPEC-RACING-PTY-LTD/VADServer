package vdcrypt

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
)

// Epoch chain, contract rev 1.2 (§4.4):
//
//	chain_e     = seal(K_chain_e, "chain",     {"v":1,"epoch":e,"prev_rmk":…|null})
//	metachain_e = seal(K_meta_e,  "metachain", {"v":1,"epoch":e,"prev_k_meta":…|null})
//
// Both are written for every epoch. Only full-key holders can derive
// K_chain_e, so a metadata-escrow server (K_meta only) walks the meta-chain
// and never reaches any RMK or K_data.

var (
	ErrChain        = errors.New("vdcrypt: invalid chain object")
	ErrChainMissing = errors.New("vdcrypt: no chain object links to an earlier epoch")
)

// ChainLink is a decoded full chain object. For epoch 1 HasPrev is false.
type ChainLink struct {
	Epoch   uint32
	HasPrev bool
	PrevRMK Key
}

// Wipe zeroes the key material.
func (l *ChainLink) Wipe() { l.PrevRMK.Wipe() }

// MetaChainLink is a decoded meta-chain object.
type MetaChainLink struct {
	Epoch     uint32
	HasPrev   bool
	PrevKMeta Key
}

// Wipe zeroes the key material.
func (l *MetaChainLink) Wipe() { l.PrevKMeta.Wipe() }

type chainWire struct {
	V       int    `json:"v"`
	Epoch   uint32 `json:"epoch"`
	PrevRMK []byte `json:"prev_rmk"`
}

type metaChainWire struct {
	V         int    `json:"v"`
	Epoch     uint32 `json:"epoch"`
	PrevKMeta []byte `json:"prev_k_meta"`
}

func chainAD(repo RepoID) []byte     { return adBase(PurposeChain, repo) }
func metaChainAD(repo RepoID) []byte { return adBase(PurposeMetaChain, repo) }

func checkPrevEpoch(cur uint32, hasPrev bool) error {
	if cur == 0 || hasPrev != (cur > 1) {
		return ErrInvalidEpoch
	}
	return nil
}

// SealChain writes chain_e under K_chain_e. prev is nil only for epoch 1
// and must otherwise be epoch e-1.
func SealChain(rnd io.Reader, repo RepoID, cur EpochSecret, prev *EpochSecret) ([]byte, error) {
	if err := checkPrevEpoch(cur.Epoch, prev != nil); err != nil {
		return nil, err
	}
	w := chainWire{V: 1, Epoch: cur.Epoch}
	if prev != nil {
		if prev.Epoch != cur.Epoch-1 {
			return nil, ErrInvalidEpoch
		}
		w.PrevRMK = append([]byte(nil), prev.RMK[:]...)
		defer clear(w.PrevRMK)
	}
	ck, err := cur.Keys(repo)
	if err != nil {
		return nil, err
	}
	defer ck.Wipe()
	pt, err := marshalJSON(w)
	if err != nil {
		return nil, err
	}
	defer clear(pt)
	return Seal(rnd, ck.Chain, cur.Epoch, pt, chainAD(repo))
}

// SealMetaChain writes metachain_e under K_meta_e. prevKMeta is nil only
// for epoch 1 and must otherwise be K_meta_{e-1}.
func SealMetaChain(rnd io.Reader, repo RepoID, cur EpochKeys, prevKMeta *Key) ([]byte, error) {
	if err := checkPrevEpoch(cur.Epoch, prevKMeta != nil); err != nil {
		return nil, err
	}
	w := metaChainWire{V: 1, Epoch: cur.Epoch}
	if prevKMeta != nil {
		w.PrevKMeta = append([]byte(nil), prevKMeta[:]...)
		defer clear(w.PrevKMeta)
	}
	pt, err := marshalJSON(w)
	if err != nil {
		return nil, err
	}
	defer clear(pt)
	return Seal(rnd, cur.Meta, cur.Epoch, pt, metaChainAD(repo))
}

// decodeLink strictly parses a chain payload: exactly v, epoch and the
// one named prev field (base64 32 bytes or null).
func decodeLink(pt []byte, epoch uint32, prevField string) (Key, bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(pt, &m); err != nil || len(m) != 3 {
		return Key{}, false, ErrChain
	}
	var v int
	var e uint32
	raw, ok := m[prevField]
	if !ok || json.Unmarshal(m["v"], &v) != nil || v != 1 ||
		json.Unmarshal(m["epoch"], &e) != nil || e != epoch {
		return Key{}, false, ErrChain
	}
	if bytes.Equal(raw, []byte("null")) {
		if epoch != 1 {
			return Key{}, false, ErrChain
		}
		return Key{}, false, nil
	}
	var b []byte
	if err := json.Unmarshal(raw, &b); err != nil {
		return Key{}, false, ErrChain
	}
	defer clear(b)
	k, err := KeyFromBytes(b)
	if err != nil || epoch == 1 {
		return Key{}, false, ErrChain
	}
	return k, true, nil
}

// OpenChain opens chain_e with K_chain_e.
func OpenChain(repo RepoID, epoch uint32, kChain Key, sealed []byte) (ChainLink, error) {
	pt, err := Open(kChain, epoch, sealed, chainAD(repo))
	if err != nil {
		return ChainLink{}, err
	}
	defer clear(pt)
	k, has, err := decodeLink(pt, epoch, "prev_rmk")
	if err != nil {
		return ChainLink{}, err
	}
	return ChainLink{Epoch: epoch, HasPrev: has, PrevRMK: k}, nil
}

// OpenMetaChain opens metachain_e with K_meta_e.
func OpenMetaChain(repo RepoID, epoch uint32, kMeta Key, sealed []byte) (MetaChainLink, error) {
	pt, err := Open(kMeta, epoch, sealed, metaChainAD(repo))
	if err != nil {
		return MetaChainLink{}, err
	}
	defer clear(pt)
	k, has, err := decodeLink(pt, epoch, "prev_k_meta")
	if err != nil {
		return MetaChainLink{}, err
	}
	return MetaChainLink{Epoch: epoch, HasPrev: has, PrevKMeta: k}, nil
}

// EpochObjects are the chain objects written for one epoch.
type EpochObjects struct {
	Chain     []byte // chain_e
	MetaChain []byte // metachain_e
}

func sealBoth(rnd io.Reader, repo RepoID, cur EpochSecret, prev *EpochSecret) (EpochObjects, error) {
	chain, err := SealChain(rnd, repo, cur, prev)
	if err != nil {
		return EpochObjects{}, err
	}
	ck, err := cur.Keys(repo)
	if err != nil {
		return EpochObjects{}, err
	}
	defer ck.Wipe()
	var prevMeta *Key
	if prev != nil {
		pk, err := prev.Keys(repo)
		if err != nil {
			return EpochObjects{}, err
		}
		defer pk.Wipe()
		prevMeta = &pk.Meta
	}
	meta, err := SealMetaChain(rnd, repo, ck, prevMeta)
	if err != nil {
		return EpochObjects{}, err
	}
	return EpochObjects{Chain: chain, MetaChain: meta}, nil
}

// InitEpoch creates epoch 1 with chain_1 and metachain_1 (§4.7 step 3).
func InitEpoch(rnd io.Reader, repo RepoID) (EpochSecret, EpochObjects, error) {
	rmk, err := RandomKey(rnd)
	if err != nil {
		return EpochSecret{}, EpochObjects{}, err
	}
	s := EpochSecret{Epoch: 1, RMK: rmk}
	objs, err := sealBoth(rnd, repo, s, nil)
	if err != nil {
		s.Wipe()
		return EpochSecret{}, EpochObjects{}, err
	}
	return s, objs, nil
}

// Rotate creates epoch cur.Epoch+1 (§4.5 steps 1–2): a fresh RMK plus
// chain_{e+1} and metachain_{e+1}. The caller writes both objects and the
// wraps for the current recipients, then switches writers.
func Rotate(rnd io.Reader, repo RepoID, cur EpochSecret) (EpochSecret, EpochObjects, error) {
	if cur.Epoch == 0 || cur.Epoch == math.MaxUint32 {
		return EpochSecret{}, EpochObjects{}, ErrInvalidEpoch
	}
	rmk, err := RandomKey(rnd)
	if err != nil {
		return EpochSecret{}, EpochObjects{}, err
	}
	next := EpochSecret{Epoch: cur.Epoch + 1, RMK: rmk}
	objs, err := sealBoth(rnd, repo, next, &cur)
	if err != nil {
		next.Wipe()
		return EpochSecret{}, EpochObjects{}, err
	}
	return next, objs, nil
}

// ResolveEpochs walks the full chain from top with K_chain and returns the
// secrets of every epoch 1..top.Epoch. chains may hold any chain/ objects
// in any order; objects that do not open as chain_e are skipped.
func ResolveEpochs(repo RepoID, top EpochSecret, chains [][]byte) (map[uint32]EpochSecret, error) {
	if top.Epoch == 0 {
		return nil, ErrInvalidEpoch
	}
	out := map[uint32]EpochSecret{top.Epoch: top}
	cur := top
	for cur.Epoch > 1 {
		ck, err := cur.Keys(repo)
		if err != nil {
			return nil, err
		}
		found := false
		for _, c := range chains {
			if e, err := EnvelopeEpoch(c); err != nil || e != cur.Epoch {
				continue
			}
			l, err := OpenChain(repo, cur.Epoch, ck.Chain, c)
			if err != nil {
				continue
			}
			cur = EpochSecret{Epoch: cur.Epoch - 1, RMK: l.PrevRMK}
			out[cur.Epoch] = cur
			found = true
			break
		}
		ck.Wipe()
		if !found {
			return nil, ErrChainMissing
		}
	}
	return out, nil
}

// WalkMeta walks the meta-chain with only K_meta (metadata escrow) and
// returns K_meta for every epoch 1..epoch. It never touches full chain
// objects, which K_meta cannot open.
func WalkMeta(repo RepoID, epoch uint32, kMeta Key, chains [][]byte) (map[uint32]Key, error) {
	if epoch == 0 {
		return nil, ErrInvalidEpoch
	}
	out := map[uint32]Key{epoch: kMeta}
	for e := epoch; e > 1; e-- {
		found := false
		for _, c := range chains {
			if ee, err := EnvelopeEpoch(c); err != nil || ee != e {
				continue
			}
			l, err := OpenMetaChain(repo, e, out[e], c)
			if err != nil {
				continue
			}
			out[e-1] = l.PrevKMeta
			found = true
			break
		}
		if !found {
			return nil, ErrChainMissing
		}
	}
	return out, nil
}
