# Contract: VaultDock Repository Format v1 (LOCKED)

Status: **LOCKED, contract revision 1.2**, 2026-10-18. Supersedes revision 1.0 of 2026-10-17, which was never implemented (see §14). Owner: INT (master agent).

Implemented by SDK-REPO in the shared SDK module (`VaultDock/sdk/{vdcrypt,compress,chunker,pack,snapshot,repo}`). The server reads metadata and, under full escrow, data. The Windows client and `vaultdock-restore.exe` read and write.

This contract defines the bytes in a repository. Every VaultDock build and the standalone restore tool must read every repository written under on-disk format version 1 **forever**. Sub-agents implement it exactly. If they find a defect, they report it under "Contract issues" in their `SUBAGENT-*.md` and do not improvise. Only INT amends this file (§14).

Notation: `||` = concatenation; `u8/u32/u64` = unsigned little-endian integers; `hex` = lowercase hex; `b64` = standard base64 with padding; `JCS` = RFC 8785 JSON Canonicalization Scheme; `seal`/`open` = §3.2.

---

## 1. Principles (owner decision 1: shared key material, browsable metadata)

1. **All repository content is encrypted client-side** before upload. The storage backend (the server's local disks, NAS, S3/R2/B2 behind the server) only ever holds ciphertext, plus the plaintext `config` and key-file envelopes.
2. **Key material is shared between the user's devices and the VaultDock server** through *escrow* (§4), established at device registration. The server can therefore:
   - decrypt **metadata** (trees, inventories, indexes, snapshot roots), so the web UI can browse and search; and
   - with `escrow_mode = full` (the default), also decrypt **data**, for browser download/preview, server-side verification and compaction.
   - With `escrow_mode = metadata`, the server can never decrypt file contents.
3. **No plaintext master secret is ever transmitted or stored.** Every copy of a key is a *wrap*: an X25519 sealed box to a recipient public key (§4). Unwrapping happens only in memory on a recipient.
4. **Self-describing:** config + key files + epoch chain + indexes + snapshots + packs are sufficient to restore with no VaultDock server and no SQLite catalog, using the user's recovery key (or passphrase).
5. Every stored object except `config` is named by `oid = hex(SHA-256(stored bytes))`, so the server and storage verify integrity without keys.

---

## 2. Layout

```
repo/<repo_uuid>/config                       plaintext JSON (§5)
repo/<repo_uuid>/keys/<oid>                   plaintext JSON key files (wraps) (§4.3)
repo/<repo_uuid>/chain/<oid>                  sealed epoch-chain objects (§4.4)
repo/<repo_uuid>/index/<oid>                  sealed index (§7.4)
repo/<repo_uuid>/snapshots/<oid>              sealed snapshot root (§8.1)
repo/<repo_uuid>/data/<oid[0:2]>/<oid>        packs (§7.3)
repo/<repo_uuid>/locks/<oid>                  sealed lock objects (§10)
```

`repo_uuid` is a lowercase canonical UUIDv4. Its 16 raw bytes are written `uuid_bytes`. A repository belongs to exactly one VaultDock user.

---

## 3. Primitives, keys, envelope

### 3.1 Primitives

| Purpose | Primitive |
|---|---|
| Hash / oid | SHA-256 |
| AEAD | XChaCha20-Poly1305 (`golang.org/x/crypto/chacha20poly1305.NewX`), 24-byte random nonce from `crypto/rand` |
| KDF | HKDF-SHA256 |
| Keyed ids | HMAC-SHA256 |
| Passphrase KDF | Argon2id |
| Wraps | X25519 sealed box (§4.2) |
| Signatures | Ed25519 (the device's existing VD1 key; the server identity key SIK) |
| Compression | Zstandard (`klauspost/compress/zstd`) |

### 3.2 Key hierarchy

- **Dedup key `DK`**: 32 random bytes, generated once at repository init and never rotated in v1. Derived keys:
  - `K_id = HKDF(DK, salt=uuid_bytes, info="vaultdock.repo/1 id")`: chunk/blob ids.
  - `K_chunker = HKDF(DK, …"chunker")`: the gear table.
  - `K_fh = HKDF(DK, …"filehash")`: keyed whole-file hashes.
  - `K_mac = HKDF(DK, …"config-mac")`: config integrity.
- **Epoch key `RMK_e`**: 32 random bytes per epoch `e = 1, 2, …`. Derived keys, with `salt = uuid_bytes || u32(e)`:
  - `K_data_e = HKDF(RMK_e, …"data")`: data blobs and data-pack headers.
  - `K_meta_e = HKDF(RMK_e, …"meta")`: tree/meta blobs, metadata-pack headers, indexes, snapshot roots, locks, meta-chain objects.
  - `K_chain_e = HKDF(RMK_e, …"chain")`: full epoch-chain objects only (rev 1.2). Never held by a metadata-escrow server.
- New data is always written under the **current epoch**. Old epochs stay readable through the chain (§4.4).

### 3.3 Sealed envelope

```
sealed = 0x01 || u32(epoch) || nonce[24] || ciphertext_with_tag[16]
```
`0x01` = envelope version. The overhead is 45 bytes.

The key is chosen by content class: `K_data_e`, `K_meta_e` or `K_chain_e`. Associated data (AD) is the ASCII string `"vd1:" || purpose || ":" || repo_uuid`, plus a suffix where noted:

| purpose | key | AD suffix |
|---|---|---|
| `blob` (data) | K_data_e | `":" || hex(blob_id)` |
| `blob` (tree/meta) | K_meta_e | `":" || hex(blob_id)` |
| `packhdr` | K_data_e (data pack) / K_meta_e (meta pack) | `":" || hex(pack_nonce)` |
| `index`, `snapshot`, `lock`, `metachain` | K_meta_e | none |
| `chain` | K_chain_e | none |

---

## 4. Recipients, wraps, escrow, rotation, recovery

### 4.1 Recipients

Each recipient is an X25519 public key:

| Recipient kind | Private key held by | Receives |
|---|---|---|
| `device` | The Windows agent. `box_priv` is stored machine-scope DPAPI in `%ProgramData%\VaultDock\keys` (ACL SYSTEM + Administrators) | Full bundle (§4.3) |
| `escrow` | The VaultDock server: **SEK** (server escrow key) private, stored sealed by the server KEK (§4.6) | Full bundle (`escrow_mode=full`) or meta bundle (`escrow_mode=metadata`) |
| `recovery` | The user. `recovery_priv = X25519 clamp(HKDF(recovery_secret, salt=uuid_bytes, info="vaultdock.repo/1 recovery-x25519"))` | Full bundle |
| `passphrase` (optional) | The user. `priv = X25519 clamp(Argon2id(passphrase_NFC, salt, t≥3, m≥64 MiB, p=4, L=32))` | Full bundle |

The recovery and passphrase recipients are asymmetric. Any key holder can therefore wrap a **new epoch** to them without knowing the secret, which is what makes rotation possible.

### 4.2 Sealed box

The writer generates an ephemeral pair `(e_priv, e_pub)` and computes:
```
shared  = X25519(e_priv, R_pub)
wrapKey = HKDF(shared, salt=uuid_bytes, info="vaultdock.repo/1 wrap" || kind || e_pub || R_pub)
wrapped = seal_raw(wrapKey, payload, AD="vd1:keywrap:<repo_uuid>:<key_id>")
```
`seal_raw` = XChaCha20-Poly1305 with the 24-byte nonce prepended.

### 4.3 Key files (`keys/<oid>`), plaintext JSON

```json
{"v":1,"repo":"<uuid>","key_id":"<32 hex random>","kind":"device|escrow|recovery|passphrase",
 "epoch":3,"created":0,"label":"DESKTOP-ABC",
 "recipient_pub":"<b64 X25519>","epk":"<b64 ephemeral pub>",
 "kdf":{"alg":"argon2id","t":3,"m_kib":65536,"p":4,"salt":"<b64>"},
 "device":{"id":3,"sign_pub":"<b64 Ed25519>"},
 "server":{"sik_pub":"<b64 Ed25519>","sek_sig":"<b64 SIK signature over sek pub || u32(sek_version)>","sek_version":1},
 "wrapped":"<b64>"}
```
`kdf` appears only for `passphrase`, `device` only for `device`, and `server` only for `escrow`.

Bundle payloads (the plaintext inside `wrapped`, encoded as JSON):
- **Full bundle:** `{"epoch":e,"rmk":"<b64 32>","dk":"<b64 32>"}`
- **Meta bundle:** `{"epoch":e,"k_meta":"<b64 32>"}`. Metadata escrow only; the server never receives `RMK_e` or `DK`.

**Rules:**
- Every epoch has wraps for: every active device, the escrow recipient, and the recovery recipient. Passphrase wraps are optional.
- A repository without a recovery wrap for the current epoch is **not ready** (readiness check `recovery_key_confirmed` fails).

### 4.4 Epoch chain (`chain/<oid>`)

```
chain_e     = seal(K_chain_e, purpose "chain",     JSON{"v":1,"epoch":e,"prev_rmk":"<b64 RMK_{e-1}>"|null})
metachain_e = seal(K_meta_e,  purpose "metachain", JSON{"v":1,"epoch":e,"prev_k_meta":"<b64 K_meta_{e-1}>"|null})
```
The chains link **backward only**: a holder of epoch `e` can reach all earlier epochs. Holding an old epoch never grants a newer one.

Both objects are written for every epoch, under `chain/<oid>`. The full chain needs `RMK_e` (to derive `K_chain_e`), so only full-key holders (devices, recovery, passphrase, and the server under full escrow) can walk it. A metadata-escrow server holds only `K_meta_e` and can walk only the meta-chain, so it can never reach any epoch's `K_data`. This preserves §1.2.

### 4.5 Rotation

A new epoch `e+1` is created by any holder of the full key: the server under full escrow, or an authorised device. Triggers:
- device revocation (mandatory);
- `escrow_mode` change;
- SEK rotation;
- a manual request;
- the policy interval (default 365 days).

Steps:
1. Generate `RMK_{e+1}`.
2. Write `chain_{e+1}` and `metachain_{e+1}`.
3. Write wraps for all *current* recipients only. Revoked devices are excluded.
4. Switch writers to the new epoch.

Existing data is **not** re-encrypted. A revoked device can still read data from epochs it held; that is an inherent limitation. Optional re-encryption of old epochs is a compaction job (the server under full escrow, a device otherwise).

### 4.6 Server-side key custody (normative for the server)

- **Server KEK**: 32 random bytes in a key file outside the database (`VAULTDOCK_KEK_FILE`, default `/state/keys/kek`; renamed in rev 1.2, mode 0600, or a Docker secret). Generated by `install.sh`, which also prints a one-time **server key backup** instruction.
- **SIK** (server identity, Ed25519) and **SEK** (server escrow, X25519) private keys are stored only sealed by the server KEK. SEK is versioned. Each SEK public key is signed by SIK.
- Devices **pin the SIK fingerprint at enrolment**: it is shown next to the verification code in the device and the web UI. A SEK rotation is accepted only if signed by the pinned SIK.
- The server unwraps bundles only in memory and only for:
  - (a) catalog ingestion of metadata;
  - (b) re-wrapping to a newly approved device of the same user;
  - (c) full-escrow data operations (browser download, verify, compaction);
  - (d) rotation.
- Go cannot guarantee memory zeroisation. Buffers are wiped best-effort and lifetimes are minimised (documented limitation).

### 4.7 Key exchange at device registration

1. The device generates `box_priv/box_pub` (X25519) alongside its existing Ed25519 key and sends `box_pub` in `POST /device/enroll` (api-v1 §5A).
2. On user approval, the server returns the SIK pub (to be pinned) and the SEK pub plus its signature.
3. **First device of the user:** the device generates `DK`, `RMK_1` and the recovery secret. It writes `config`, `chain_1` and the device, escrow and recovery wraps, then shows the recovery key once with confirmation (`VDRK1-` text, §4.8).
4. **Additional device:**
   - Under full escrow, the server unwraps the escrow bundle in memory and writes a `device` wrap for the new device.
   - Under metadata escrow, an existing online device does this through a `key_share` device task, or the user enters the recovery key on the new device.
5. **Reinstalled Windows / lost device:** the new device enrols normally; step 4 applies. With no server, the recovery key (or passphrase) opens the repository directly.

### 4.8 Recovery key text

`"VDRK1-"` followed by Crockford-base32 groups of 5 characters joined by `-`. The encoded bytes are `recovery_secret[32] || SHA-256(recovery_secret)[0:2]`. Decoding is case-insensitive, accepts `O→0` and `I/L→1`, and ignores whitespace and `-`. A checksum mismatch is a user-facing error.

---

## 5. Config (`config`), plaintext JSON

```json
{"format":"vaultdock.repo","v":1,"repo":"<uuid>","created":0,
 "escrow_mode":"full|metadata",
 "chunker":{"alg":"vd-fastcdc-1","min":524288,"avg":1048576,"max":8388608},
 "compression":"zstd","aead":"xchacha20poly1305","pack_target":16777216,"pack_max":67108864,
 "recovery_pub":"<b64 X25519>",
 "mac":"<hex HMAC-SHA256(K_mac, JCS(config without mac))>"}
```

Readers:
1. Reject an unknown `format` or a higher `v`.
2. Unwrap a full bundle.
3. Verify `mac`.

Metadata-only holders (the server under metadata escrow) cannot verify `mac`. They treat `config` as untrusted, accept only the documented fields, and rely on device-signed snapshot roots (§8.1).

Changing `escrow_mode` rewrites `config` (exclusive lock) and starts a new epoch.

---

## 6. Path tokens

Unchanged from revision 1.0. Paths are token + `/`-separated components:

| Tokens | Windows source |
|---|---|
| `{PROFILE}` `{DESKTOP}` `{DOCUMENTS}` `{DOWNLOADS}` `{PICTURES}` `{MUSIC}` `{VIDEOS}` `{SAVEDGAMES}` | The matching user `FOLDERID_*` |
| `{APPDATA}` `{LOCALAPPDATA}` `{LOCALLOW}` | `FOLDERID_RoamingAppData`, `FOLDERID_LocalAppData`, `FOLDERID_LocalAppDataLow` |
| `{PROGRAMDATA}` `{PROGRAMFILES}` `{PROGRAMFILESX86}` | Machine `FOLDERID_*` |
| `{VOL:<volume-guid>}` | Any other path |

Writers pick the longest matching token. The snapshot root records the concrete value of every token used.

---

## 7. Blobs, packs, indexes

### 7.1 Blob types

`0` data, `1` tree, `2` meta.

### 7.2 Blob encoding

- `blob_id = HMAC-SHA256(K_id, raw_plaintext)`.
- `payload = comp_u8 || body`, where `comp = 0` stores raw bytes and `comp = 1` stores zstd level 3. Writers store raw when the compressed form is ≥ 0.97 × raw, or for known-compressed types.
- `sealed_blob = seal(K_data_e | K_meta_e, payload, AD="vd1:blob:<uuid>:<hex(blob_id)>")`.
- Raw size limits: data ≤ 8 MiB; tree and meta ≤ 32 MiB. Decoders never inflate beyond `raw_len`.

### 7.3 Pack

```
pack    = sealed_blob_0 || … || sealed_blob_n || pack_nonce[16] || sealed_header || trailer
trailer = u32(16 + len(sealed_header)) || "VDPACK" || 0x00 || 0x01
```
- `header_plain = u8(class: 0 data, 1 meta) || u32(count) || count × (type u8 || blob_id[32] || offset u32 || sealed_len u32 || raw_len u32)`.
- `sealed_header = seal(K_data_e` (data packs) `| K_meta_e` (meta packs)`, header_plain, AD="vd1:packhdr:<uuid>:<hex(pack_nonce)>")`.
- Data packs hold type 0 only. Meta packs hold types 1 and 2. Blob order is shuffled. Packs are sealed at ≥ `pack_target`, at 30 s idle, or at job end, and never exceed `pack_max`.
- Stored at `data/<oid[0:2]>/<oid>`.

### 7.4 Index

```
index = seal(K_meta_e, 0x01 || zstd(JSON))
```
The JSON is:
```json
{"v":1,"supersedes":["<oid>"],"packs":[{"pack":"<oid>","class":0,"blobs":[{"id":"<hex>","t":0,"o":0,"l":0,"r":0}]}]}
```
Readers take the union of all indexes; the first match wins.

---

## 8. Snapshots, trees, metadata

### 8.1 Snapshot root

```
snapshot = seal(K_meta_e, 0x01 || zstd(JSON))
```
The JSON fields are the same as revision 1.0, plus:
- `"epoch":e`
- `"profile":{"sid":"…","username":"…","consent_id":"…"}`: the authorised Windows profile (owner decision 6).
- `"install_mode":"simple|advanced"`: informational.

The `sig` field is an Ed25519 signature by the device over `JCS(root without sig)`. A bad signature is a hard error on restore; a missing device key file is a warning.

### 8.2 Tree blob

Same as revision 1.0, except:
- `h` = **`HMAC-SHA256(K_fh, whole file)`** instead of plain SHA-256. A metadata-escrow server can then browse names and sizes but cannot run confirmation-of-file attacks.
- Security descriptors are never recorded.
- Entries are sorted bytewise by `n`, with a `cont` split at 50 000 entries.

### 8.3 Meta blobs

Same as revision 1.0 (`inventory/1`, `registry/1`).

---

## 9. Chunker `vd-fastcdc-1`

Unchanged from revision 1.0:
- `gear[i] = u64_le(HMAC-SHA256(K_chunker, "gear" || u8(i))[0:8])`;
- min 512 KiB, avg 1 MiB, max 8 MiB;
- `maskS = 0xFFFFFC0000000000` before avg, `maskL = 0xFFFFC00000000000` after;
- `h = (h << 1) + gear[b]` (wrapping), starting at `p + min`; cut when `h & mask == 0`; hard cut at max;
- per-file, so files < min form one chunk.

Library use is permitted only if it is bit-exact against §13.

---

## 10. Locks

```
lock = seal(K_meta_e, 0x01 || JSON{"v":1,"holder":{…},"kind":"shared|exclusive","created":0,"expires":0})
```
For repositories behind the VaultDock server (**the only supported topology**; direct-to-cloud is out of scope), **server API locks are authoritative**. Lock objects are written only by `vaultdock-restore` or maintenance tooling operating offline on a repository copy.

---

## 11. Ordering and GC invariants

Unchanged from revision 1.0:
1. Write order: data packs → index → snapshot.
2. GC reads snapshots → indexes → packs.
3. Grace period ≥ 24 h.
4. Two-phase pending-delete. A re-PUT clears the mark.
5. Forget = delete the snapshot object.
6. Protected snapshots are never forgotten.
7. Compaction takes an exclusive lock.

New:
- Compaction of partly-live packs is done **by the server under full escrow**, or by a device task under metadata escrow.
- Superseded chain or key objects are never deleted while any snapshot of that epoch exists.

---

## 12. Versioning and compatibility

As revision 1.0. The envelope's epoch field is mandatory from revision 1.1. Because revision 1.0 was never implemented, no reader needs to handle the 1.0 envelope.

---

## 13. Required test vectors (`sdk/repo/testdata/v1/`)

Fixed inputs: `DK = 0x00..0x1f`, `RMK_1 = 0x20..0x3f`, `RMK_2 = 0x40..0x5f`, `repo_uuid = 00000000-0000-4000-8000-000000000000`, recovery secret `0x60..0x7f`, device box seed `0x80..0x9f`, SEK seed `0xa0..0xbf`, Ed25519 device seed `0xc0..0xdf`, SIK seed `0xe0..0xff`, fixed nonces and ephemerals via a test RNG.

1. All subkeys.
2. Gear table excerpt and chunk boundaries of a 32 MiB SHA-256-counter stream (seed `"vaultdock"`).
3. Sealed blobs (data under epoch 1, meta under epoch 2).
4. One data pack and one meta pack, with their oids.
5. An index; a signed snapshot root.
6. Key files for device, escrow (full and meta), recovery, and passphrase (test parameters `t=1, m=8 MiB`).
7. `chain_2`.
8. Recovery key text round trip.
9. Config mac.

---

## 14. Amendments

- **Rev 1.1 (2026-10-18, INT), owner decisions 1 and 9.** Rev 1.0 was never implemented, so no data migration is needed.
  - Replaced "client-only keys + optional plaintext listing" with **escrow-based shared key material** (§4).
  - Split epoch keys into `K_data`/`K_meta` and added epochs, the backward chain, asymmetric recovery and passphrase recipients, and server custody rules.
  - Keyed the whole-file hash `h`.
  - Removed direct-to-destination lock usage.
  - The envelope gains the epoch field.
- **Rev 1.2 (2026-10-18, INT), security fix found by P1-01 before any repository was written.** In rev 1.1 the full chain (with `prev_rmk`) was sealed under `K_meta_e`, so a metadata-escrow server could recover older `RMK`s and decrypt file contents, which violated §1.2.
  - Added `K_chain_e` (§3.2).
  - The full chain is sealed under it with AD purpose `chain`.
  - The meta-chain (`prev_k_meta` only) is sealed under `K_meta_e` with AD purpose `metachain`, and both are always written (§4.4).
  - Test vector 7 is regenerated.
  - No data migration is needed (nothing was written under rev 1.1).
