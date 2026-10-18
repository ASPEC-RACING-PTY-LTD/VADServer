<!-- Historical text of contract revision 1.0, restored by INT on 2026-10-18 from the planning session. Superseded by rev 1.1, which references it for fields marked "as rev 1.0". Read-only reference. -->

# Contract: VaultDock Repository Format v1 (LOCKED)

Status: **LOCKED v1.0**, 2026-10-17. Owner: INT (master agent). Implemented by PKG-REPO in `pkg/{vdcrypt,compress,chunker,pack,snapshot,repo}`.

This contract defines the bytes on disk/in object storage. Any VaultDock build, and the standalone `vaultdock-restore.exe`, must be able to read every repository written under this version **forever**. Sub-agents implement it exactly. If they find a defect, they report it under "Contract issues" in their `SUBAGENT-*.md` and do not improvise a change. Only INT amends this file (§14).

Notation: `||` = concatenation; `u8/u32` = unsigned little-endian integers; `hex` = lowercase hex; `b64` = standard base64 with padding; `JCS` = RFC 8785 JSON Canonicalization Scheme.

---

## 1. Principles

1. **Encryption is mandatory in format v1.** "Managed-metadata" mode (api-v1 §5.3) is a separate plaintext listing sent to the VaultDock server only. It is never stored in the repository.
2. The repository is **self-describing**: config + key files + indexes + snapshots + packs are sufficient to restore with no VaultDock server and no SQLite catalog.
3. The storage backend sees only: object names (hashes), sizes, the plaintext `config` and the plaintext key-file envelopes. Chunk ids, file names, sizes of individual files and tree structure are always encrypted.
4. Every stored object except `config` is named by `oid = hex(SHA-256(stored bytes))`, so the server and storage can verify integrity without keys.

---

## 2. Layout

All keys are relative to the backend root:

```
repo/<repo_uuid>/config                       (fixed name, plaintext JSON, §5)
repo/<repo_uuid>/keys/<oid>                   (plaintext JSON key files, §4)
repo/<repo_uuid>/index/<oid>                  (sealed index, §7.4)
repo/<repo_uuid>/snapshots/<oid>              (sealed snapshot root, §8.1)
repo/<repo_uuid>/data/<oid[0:2]>/<oid>        (packs, §7.3)
repo/<repo_uuid>/locks/<oid>                  (sealed lock objects, §10)
```

`repo_uuid` is a lowercase canonical UUIDv4 string. Its 16 raw bytes are written `uuid_bytes` below.

---

## 3. Primitives and key hierarchy

| Purpose | Primitive |
|---|---|
| Hash / object id | SHA-256 |
| AEAD | XChaCha20-Poly1305 (`golang.org/x/crypto/chacha20poly1305.NewX`), 24-byte random nonce from `crypto/rand`, 16-byte tag |
| KDF (subkeys) | HKDF-SHA256 |
| Chunk id | HMAC-SHA256 |
| Passphrase KDF | Argon2id |
| Device wrap | X25519 + HKDF |
| Snapshot signature | Ed25519 (the device's existing VD1 signing key) |
| Compression | Zstandard (`klauspost/compress/zstd`) |

**Master key** `MK`: 32 random bytes per repository, generated once at init.

**Subkeys:** `K_x = HKDF-SHA256(ikm=MK, salt=uuid_bytes, info="vaultdock.repo/1 " || label, L=32)`, with these labels:

| Label | Key | Use |
|---|---|---|
| `enc` | `K_enc` | AEAD key for all sealed data |
| `id` | `K_id` | Chunk/blob ids |
| `chunker` | `K_chunker` | Gear table (§9) |
| `config-mac` | `K_mac` | Config integrity (§5) |

**Sealed envelope** (every encrypted thing):
```
sealed = 0x01 || nonce[24] || ciphertext_with_tag
```
`0x01` = envelope version. Overhead is 41 bytes.

**Associated data** (AD) is the ASCII string `"vd1:" || purpose || ":" || repo_uuid`, plus a suffix where noted:

| purpose | used for | suffix |
|---|---|---|
| `blob` | blobs inside packs | `":" || hex(blob_id)` |
| `packhdr` | pack headers | `":" || hex(pack_nonce)` (see §7.3) |
| `index` | index objects | none |
| `snapshot` | snapshot roots | none |
| `lock` | lock objects | none |
| `keywrap` | key files | `":" || key_id` |

---

## 4. Key files (`keys/<oid>`), plaintext JSON

```json
{"v":1,"repo":"<repo_uuid>","key_id":"<32 hex random>","kind":"recovery|passphrase|device",
 "epoch":1,"created":1760000000000,"label":"Recovery key",
 "kdf":{"alg":"argon2id","t":3,"m_kib":65536,"p":4,"salt":"<b64 16 bytes>"},
 "epk":"<b64 32-byte X25519 ephemeral public key>",
 "device":{"id":3,"name":"DESKTOP-ABC","sign_pub":"<b64 Ed25519 pub>","box_pub":"<b64 X25519 pub>"},
 "wrapped":"<b64 sealed MK>"}
```
`kdf` appears only for `passphrase`. `epk` and `device` appear only for `device`. `wrapped = seal(wrapKey, MK, AD="vd1:keywrap:<repo_uuid>:<key_id>")`.

Wrap keys:
- **recovery:** `wrapKey = HKDF-SHA256(ikm=recovery_secret[32], salt=uuid_bytes, info="vaultdock.repo/1 wrap-recovery", L=32)`.
- **passphrase:** `wrapKey = Argon2id(passphrase_utf8_NFC, salt, t=3, m=64 MiB, p=4, L=32)`. These are minimum parameters; readers accept any `t/m_kib/p` recorded in the file.
- **device:** the writer creates an ephemeral X25519 pair and computes `shared = X25519(e_priv, device.box_pub)`, then `wrapKey = HKDF-SHA256(ikm=shared, salt=uuid_bytes, info="vaultdock.repo/1 wrap-device" || e_pub || device.box_pub, L=32)`. The device's `box_priv` is kept by the desktop under DPAPI (DSK-D) and is **never** written to the repository.

**Recovery key text:**
```
"VDRK1-" || groups of 5 Crockford-base32 chars joined by "-"
```
The encoded bytes are `recovery_secret[32] || SHA-256(recovery_secret)[0:2]`. Decoding is case-insensitive, accepts `O→0` and `I/L→1`, and ignores whitespace and `-`. A checksum mismatch is a user-facing error.

**Rules:**
- A repository must always have ≥1 `recovery` key file. Init requires creating one, and the UI requires the user to confirm it.
- Removing a device's key file revokes that device's ability to unwrap. It does not re-key (key epochs are reserved for v2; `epoch` is always 1 in v1).
- Device key files also publish `sign_pub`, so snapshot signatures (§8.1) can be verified without the server.

---

## 5. Config (`config`), plaintext JSON

```json
{"format":"vaultdock.repo","v":1,"repo":"<repo_uuid>","created":1760000000000,
 "chunker":{"alg":"vd-fastcdc-1","min":524288,"avg":1048576,"max":8388608},
 "compression":"zstd","aead":"xchacha20poly1305","pack_target":16777216,"pack_max":67108864,
 "mac":"<hex HMAC-SHA256(K_mac, JCS(config without the mac field))>"}
```

Readers:
1. Reject `format` ≠ `vaultdock.repo` or `v` > supported.
2. Unwrap MK from any key file.
3. Verify `mac`. A mismatch means the config was tampered with: stop.

Unknown extra fields are covered by the mac and ignored by the reader.

---

## 6. Path tokens

Manifests store paths relative to tokens so restores remap across users and machines. A path is `TOKEN` + `/`-separated components (forward slashes in manifests; converted on restore).

| Token | Windows source |
|---|---|
| `{PROFILE}` | `FOLDERID_Profile` |
| `{DESKTOP}` | `FOLDERID_Desktop` |
| `{DOCUMENTS}` | `FOLDERID_Documents` (may be OneDrive-redirected) |
| `{DOWNLOADS}` | `FOLDERID_Downloads` |
| `{PICTURES}`, `{MUSIC}`, `{VIDEOS}` | `FOLDERID_Pictures`, `FOLDERID_Music`, `FOLDERID_Videos` |
| `{SAVEDGAMES}` | `FOLDERID_SavedGames` |
| `{APPDATA}` | `FOLDERID_RoamingAppData` |
| `{LOCALAPPDATA}` | `FOLDERID_LocalAppData` |
| `{LOCALLOW}` | `FOLDERID_LocalAppDataLow` |
| `{PROGRAMDATA}` | `FOLDERID_ProgramData` |
| `{PROGRAMFILES}` / `{PROGRAMFILESX86}` | `FOLDERID_ProgramFiles` / `FOLDERID_ProgramFilesX86` |
| `{VOL:<volume-guid>}` | Any other path: the volume GUID (`\\?\Volume{…}\`) |

Writers pick the **longest** matching token. The snapshot root (§8.1) records the concrete value of every token used, so the restore planner can show "from → to".

---

## 7. Blobs, packs and indexes

### 7.1 Blob types

| type (u8) | Content |
|---|---|
| 0 data | File content chunk |
| 1 tree | Directory listing (§8.2) |
| 2 meta | Inventory, registry export or other structured metadata (§8.3) |

### 7.2 Blob encoding

- `blob_id = HMAC-SHA256(K_id, raw_plaintext)` (32 bytes) over the **uncompressed** bytes.
- `payload = comp_u8 || body`, where:
  - `comp = 0`: body = raw;
  - `comp = 1`: body = zstd(raw), level 3.
- Writers use `comp = 0` when `len(zstd) ≥ 0.97 × len(raw)`, or for known-compressed content (jpg, png, mp4, zip, 7z, etc., via `pkg/mimetypes`).
- `sealed_blob = seal(K_enc, payload, AD="vd1:blob:<repo_uuid>:<hex(blob_id)>")`.
- Raw size limits: data ≤ 8 MiB (the chunker max); tree and meta ≤ 32 MiB. Decoders must refuse to decompress beyond the recorded `raw_len`.

### 7.3 Pack object (`data/…`)

```
pack    = sealed_blob_0 || … || sealed_blob_n || pack_nonce[16] || sealed_header || trailer
trailer = u32(16 + len(sealed_header)) || "VDPACK" || 0x00 || 0x01
```
The trailer is 12 bytes: the length of `pack_nonce || sealed_header`, then the 8-byte magic+version `56 44 50 41 43 4B 00 01`.

- `pack_nonce` = 16 random bytes stored in clear. It binds the header to this pack through the AD.
- `header_plain = u32(count) || count × entry`, where `entry = type u8 || blob_id[32] || offset u32 || sealed_len u32 || raw_len u32` (45 bytes), and `offset` is from the pack start.
- `sealed_header = seal(K_enc, header_plain, AD="vd1:packhdr:<repo_uuid>:<hex(pack_nonce)>")`.
- To read: take the last 12 bytes, check the magic, read the length `L`, read the `L` bytes before the trailer, split off the first 16 as `pack_nonce`, and open the rest.
- A pack contains blobs of a single class: **data packs** (type 0 only) or **metadata packs** (types 1 and 2). Restore can then fetch trees without downloading data.
- Writers seal a pack at ≥ `pack_target` bytes, at 30 s idle, or at the end of a job. A pack never exceeds `pack_max`. Blob order inside a pack is shuffled before sealing.
- `oid = hex(SHA-256(pack))`, stored at `data/<oid[0:2]>/<oid>`.

### 7.4 Index object (`index/<oid>`)

```
index = seal(K_enc, 0x01 || zstd(JSON), AD="vd1:index:<repo_uuid>")
```
The JSON is:
```json
{"v":1,"supersedes":["<index oid>"],
 "packs":[{"pack":"<pack oid>","blobs":[{"id":"<hex>","t":0,"o":0,"l":0,"r":0}]}]}
```
`t` = type, `o` = offset, `l` = sealed_len, `r` = raw_len.

- Each backup run writes ≥1 index covering the packs it created.
- Compaction writes a merged index listing the superseded index oids. Superseded indexes may be deleted after the GC grace period (§11).
- **Readers union all indexes.** Duplicate blob ids across packs are allowed (first match wins).

---

## 8. Snapshots, trees, metadata

### 8.1 Snapshot root (`snapshots/<oid>`)

```
snapshot = seal(K_enc, 0x01 || zstd(JSON), AD="vd1:snapshot:<repo_uuid>")
```

```json
{"v":1,"repo":"<repo_uuid>","time":0,"parent":"<snapshot oid>|null",
 "job":{"id":12,"name":"Documents","kind":"folder","mode":"full"},
 "device":{"id":3,"name":"DESKTOP-ABC","key_id":"<device key file key_id>"},
 "host":{"hostname":"DESKTOP-ABC","os":"Windows 11 Pro 10.0.26300","user":"ali","sid":"S-1-5-21-…","machine_guid_hash":"<hex>"},
 "tokens":{"{PROFILE}":"C:\\Users\\ali","{DOCUMENTS}":"D:\\OneDrive\\Documents"},
 "roots":[{"token_path":"{DOCUMENTS}","tree":"<tree blob id hex>"}],
 "inventory":"<meta blob id hex>|null",
 "stats":{"files":0,"dirs":0,"bytes":0},"tags":[],
 "issues_summary":{"skipped":0,"by_class":{"locked":0}},
 "sig":"<b64 Ed25519 signature over JCS(root without the sig field)>"}
```
Restore verifies `sig` with `sign_pub` from the device key file named by `device.key_id`. A bad signature is a hard error; a missing key file is a warning.

### 8.2 Tree blob (type 1)

The raw content is JSON:
```json
{"v":1,"entries":[{"n":"report.docx","nb":null,"t":"f","s":0,"m":0,"c":0,"a":32,
  "h":"<sha256 hex of whole file>","ch":["<blob id hex>"],"sub":null,"ln":null,
  "x":{"ads":[{"name":"Zone.Identifier","ch":["…"],"s":0}],"placeholder":false}}],
 "cont":null}
```

| Field | Meaning |
|---|---|
| `n` | Name as UTF-8 |
| `nb` | b64 of the raw UTF-16LE name, only when the name is not losslessly representable in UTF-8 (unpaired surrogates); otherwise null |
| `t` | `f` file, `d` dir, `l` symlink, `r` other reparse point (recorded, not followed) |
| `s` | Size |
| `m` / `c` | mtime / ctime in **Unix nanoseconds** |
| `a` | Windows `FILE_ATTRIBUTE_*` bits |
| `h` | Whole-file SHA-256, verified on restore |
| `ch` | Ordered chunk ids (`[]` for an empty file) |
| `sub` | Subtree blob id for dirs |
| `ln` | Link target |
| `x` | Optional extensions; readers ignore unknown keys |

- Entries are sorted by the bytewise order of `n`.
- A directory with more than 50 000 entries is split, and `cont` holds the id of the continuation tree blob.
- **Security descriptors are never recorded** in v1 (no ACL/SID restore, D16).

### 8.3 Meta blobs (type 2)

Raw JSON with a `"schema"` discriminator:

- `"schema":"inventory/1"`:
  - `apps`: `[{name,version,publisher,source:"winget|msstore|choco|scoop|msi|exe|steam|epic|gog",package_id,match_method:"exact_product_code|winget_list|name_publisher|manual|none",tier:1..6,install_scope:"user|machine",mode,install_location,uninstall_key,flags:["not_portable","settings_before_install"],data_roots:["{APPDATA}/Vendor/App"],registry:["<meta blob id>"]}]`
  - `games`: `[{name,store,app_id,save_roots:[token paths],manifest_source:"ludusavi@<rev>"|"builtin"}]`
  - `devprojects`: `[{root:"{PROFILE}/src/x",remote,branch,head,dirty:bool,pushed:bool,mode}]`
  - `not_portable`: `[{item,reason}]`
- `"schema":"registry/1"`: `{"hive":"HKCU","key":"Software\\Vendor\\App","values":[{"n":"","type":"REG_SZ|REG_EXPAND_SZ|REG_DWORD|REG_QWORD|REG_BINARY|REG_MULTI_SZ","d":"<b64 raw bytes>"}],"subkeys":[…same shape…]}`. Only per-app HKCU keys and explicitly catalogued HKLM keys; never whole hives.

---

## 9. Chunker `vd-fastcdc-1`

The algorithm is fully specified here. It is **not** "whatever a library does", so library upgrades cannot shift boundaries.

1. Gear table: `gear[i] = u64_le(HMAC-SHA256(K_chunker, "gear" || u8(i))[0:8])` for `i = 0..255`.
2. Parameters: `min = 524288`, `avg = 1048576`, `max = 8388608`.
3. Masks (top bits set): `maskS` = top 22 bits set (`0xFFFFFC0000000000`), `maskL` = top 18 bits set (`0xFFFFC00000000000`).
4. For each chunk starting at position `p`, with remaining length `n`:
   - if `n ≤ min`: the chunk is the rest;
   - otherwise `h = 0`, and for `i` from `p+min` to `min(p+max, p+n) − 1`: `h = (h << 1) + gear[data[i]]` (wrapping u64);
   - when `i − p < avg`, cut after byte `i` if `h & maskS == 0`; otherwise cut if `h & maskL == 0`;
   - if no cut is found, the chunk ends at `min(p+max, p+n)`.
   (The cut point is exclusive: the chunk is `data[p : i+1]`.)
5. Files smaller than `min` are one chunk. Chunking is per file; chunks never span files.

PKG-REPO may use `PlakarKorp/go-cdc-chunkers` only if it reproduces this spec bit-for-bit against the golden vectors (§13). Otherwise it implements this spec directly (≈80 lines).

---

## 10. Locks (`locks/<oid>`)

```
lock = seal(K_enc, 0x01 || JSON, AD="vd1:lock:<repo_uuid>")
```
The JSON is `{"v":1,"holder":{"device_id":3,"hostname":""},"kind":"shared|exclusive","created":0,"expires":0}`.

- For repositories behind the VaultDock server, **the server API locks (api-v1 §5) are authoritative**. Lock objects are used only in direct-to-destination mode.
- Writers refresh their lock every ≤ 1/3 of its TTL.
- A lock is stale when `expires < now − 5 min`.

---

## 11. Ordering and garbage-collection invariants

1. **Write order:** data packs → index → snapshot root. A snapshot is committed (to the server catalog, or by its existence in direct mode) only after every referenced object exists.
2. **Read order for GC:** list snapshots first, then indexes, then packs. Anything written after the listing started is treated as live.
3. **Grace:** never delete an object younger than `grace` (default 24 h; server setting `grace_hours`).
4. **Two-phase delete:**
   - Cycle N marks an unreferenced pack `pending_delete`.
   - Cycle N+1 (≥ grace later) deletes it only if it is still unreferenced.
   - The server's `objects/missing` reports `pending_delete` packs as missing. A re-PUT clears the mark (api-v1 §5).
5. **Forget** = delete `snapshots/<oid>` (plus the catalog status). Protected snapshots are never forgotten.
6. **Packs still partly referenced** are kept. Only a key-holding device may repack them (`prune_compact` task), which writes new packs + a superseding index before old packs become unreferenced.
7. Compaction/prune requires an **exclusive** lock. Backups take **shared** locks.

---

## 12. Versioning and compatibility

- `config.v` is the repository major version. Readers refuse a higher major version.
- JSON objects may gain fields in minor revisions. Readers ignore unknown fields, and writers never repurpose an existing field.
- The envelope version byte (`0x01`), the pack trailer version (`0x01`) and the index/snapshot payload prefix (`0x01` = zstd JSON) each allow independent evolution.
- Every release must keep reading all earlier minor revisions. The standalone restore tool ships with every release.

---

## 13. Required test vectors (produced by PKG-REPO, frozen by INT at review)

Stored in `pkg/repo/testdata/v1/`, using fixed inputs: `MK = 0x00..0x1f`, `repo_uuid = 00000000-0000-4000-8000-000000000000`, recovery secret `0x20..0x3f`, fixed nonces injected through a test-only RNG.

1. Subkeys `K_enc`, `K_id`, `K_chunker`, `K_mac` (hex).
2. Gear table first/last 4 entries; chunk boundaries for a 32 MiB deterministic pseudo-random stream (SHA-256 counter mode, seed `"vaultdock"`).
3. Sealed blob for raw `"hello"` (comp 0) and 1 MiB zeros (comp 1).
4. A complete pack with 3 blobs; its oid.
5. Index and snapshot roots, with the snapshot signature from a fixed Ed25519 seed `0x40..0x5f`.
6. Recovery key text for the fixed secret, and its round trip; passphrase wrap with `t=1, m=8 MiB, p=1` (test parameters).
7. Config mac.

A change to any vector is a format change and requires an INT amendment.

---

## 14. Amendments

_None yet. INT appends dated, versioned entries here._
