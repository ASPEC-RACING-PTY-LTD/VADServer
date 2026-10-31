# Remote storage nodes (upload origin) — plan

Status: **proposal, awaiting approval**. Nothing here is built yet.

## Goal

Let a storage node at another site (for example a friend's Proxmox LXC or VM, reached through a Cloudflare tunnel subdomain) hold the files of chosen users:

- **Uploads go straight from the user to the node.** They never pass through the main server.
- **Downloads and playback go through the main server.** It fetches only the encrypted blocks it needs from the node, decrypts them in memory and streams them on. Nothing is stored on the main server.
- **The node never holds a key that decrypts anything.** Neither do browsers or clients. Only the main server can decrypt.
- **The main server is managed from its own admin panel.** The node's owner gets no accounts, no admin and no settings; they only install and pair the node.

### Not in scope

- Running a second full VaultDock (database, accounts, web app) on the remote machine.
- Serving playback directly from the node to browsers (rejected: it would need keys in the browser).
- Several main servers, or the node serving other VaultDock installations.

## What already exists and is reused

| Piece | Today | Change |
|---|---|---|
| `vad-node` agent (`internal/nodeagent`) | Joins a server, admin approves, heartbeats, pulls jobs (long poll), self-updates, systemd service via `install.sh install-node` | Adds an inbound HTTPS endpoint (behind the tunnel) for uploads and block reads |
| Node identity | `Authorization: Node <id>.<secret>`, secret issued on approval | Unchanged; also used to sign main → node requests |
| Node keys (`node_keys.go`) | Per-node 256-bit copy key, sealed with `state/keys/node-copies.key`, never sent to the node, separate from the KEK | Adds a per-node **key pair**; the private key is sealed the same way. The server KEK is never used. |
| Encrypted format VDR1 (`internal/replica/crypt.go`) | AES-256-GCM in 1 MiB chunks, chunks bound to the blob and their index, length-prefixed | Becomes **VDR2**: the same chunking plus a header holding the file key wrapped for the node's public key. Fixed chunk sizes make any byte range seekable. |
| Copy rules and `blob_copies` | Encrypted copies on nodes, verification, repair, read fallback | Reused for "move existing files to the node", for the fallback when the node is offline, and for an optional second copy |
| `storage_nodes.public_url` | Recorded, unused | Becomes the node's tunnel URL (for example `https://hater.example.com`) |

## Keys and encryption

- **Per-node key pair (ECDH P-256).**
  - Generated on the main server when the node is paired.
  - **Private key:** sealed with `node-copies.key` and stored only in the main database.
  - **Public key:** handed to uploaders together with each upload ticket.
  - P-256 rather than X25519 because WebCrypto supports it in every current browser, so web uploads work everywhere.
- **Per-file content key:** 32 random bytes made by the uploader.
  - The file is encrypted in VDR2 chunks with it.
  - The key is wrapped (ECDH with a one-off ephemeral key, then HKDF-SHA256, then AES-256-GCM) for the node's public key and stored in the VDR2 header and in the main database.
  - The uploader can encrypt but can never decrypt anything else. It forgets the content key after the upload.
- **Chunk binding:** as in VDR1, chunk ciphertext is bound to the file id, the chunk index and the last-chunk flag. A node that reorders, swaps, truncates or alters data is detected on read; it can't be hidden.
- **Who sees what:**

| Party | Sees |
|---|---|
| Node owner | Encrypted blocks, sizes, upload times, the user id in tickets. Never file names, folders or content. |
| Cloudflare | The same as the node owner, in transit |
| Browser / desktop client | The public key and their own plaintext only |
| Main server | Everything; the only place that can decrypt |

- **Key rotation:**
  - **New key pair:** "Rotate node key" makes one for new uploads. Existing files keep working, because each file stores which key wrapped it.
  - **Retire a key:** "Re-wrap" rewraps every file key with the new pair. It's cheap: file contents don't change.

## Pairing (the node owner's only step)

1. **Generate the code.** On the main server, the admin opens Administration → Storage → Nodes → "Pair a remote node" and enters a name and the node's public URL. The result is a **pairing secret**: single use, valid for 24 hours, shown once, with a ready-to-paste install command.
2. **Install the node.** The node owner runs the installer from the site:
   ```bash
   curl -fsSL https://raw.githubusercontent.com/ASPEC-RACING-PTY-LTD/vad-server/main/install.sh | sudo bash -s install-node
   ```
   It:
   - checks the system (Linux, systemd, root) and installs anything missing: `curl`, `ca-certificates` and `coreutils` via apt, dnf, apk or pacman;
   - asks for the **main server's HTTPS URL**, refusing `http://`, and checks that it answers as VaultDock;
   - asks for the **pairing secret** from the main server's admin panel (input hidden, never echoed or logged);
   - asks for the folder on the disk to use (for example `/mnt/hdd/vaultdock`) and checks it's writable and how much space it has;
   - asks for this node's public HTTPS URL, its tunnel address;
   - downloads the agent from the main server (checksum-verified) and installs and starts it as a systemd service;
   - waits until the main server shows the node as Ready.

   Every question can also be answered with environment variables (`VAD_NODE_SERVER_URL`, `VAD_NODE_PAIRING`, `VAD_NODE_STORAGE`, `VAD_NODE_PUBLIC_URL`), so it can run unattended.
3. **Complete the join.** The node joins with the code and is approved automatically, since the code proves the admin asked for it. It receives its secret plus the main server's **ticket-signing key** for this node (HMAC). That key can only verify upload tickets; it decrypts nothing.
4. **Check the address.** The main server checks the node's public URL (`/healthz` over the tunnel) and marks the node Ready.

- **What the node owner can do afterwards:** `vad-node status`, stop or uninstall the service, see disk use. Nothing else.
- **Still available:** the existing manual approve flow for nodes joined without a code.

## Assigning users

- **Per user:** Administration → Users → user → "Upload origin: Main server / *node name*".
- **Per folder (phase 2):** a folder subtree can be assigned to a node, overriding the user setting.
- **Existing files** stay where they are, unless the admin presses "Move existing files to *node*":
  - a background move (copy, verify, then delete locally);
  - progress and bytes moved are shown;
  - it's resumable and can be cancelled.
- **Space:** the node reports free space in each heartbeat.
  - Below a set limit, new uploads for its users fall back to the main server (see Failure cases).
  - An admin warning is raised.

## Upload flow (direct to the node)

1. **Start.** The client sends `POST /api/v1/uploads` to **main**, as today. Main checks permissions, quota, upload blocks and duplicates. For a user whose upload origin is a node that is Ready and has space, the reply adds:
   ```
   mode: "direct", node_url, ticket, public_key {key_id, P-256 SPKI}, chunk_size
   ```
   - The ticket is signed by main with the node's ticket-signing key and is self-contained, so the node verifies it offline. It holds: upload id, user id, maximum size, expiry (6 hours), node id.
2. **Duplicates.** The client hashes the plaintext first. If main already has that content for the user, the upload is skipped, the same as today.
3. **Encrypt.** The client makes a content key, encrypts in VDR2 chunks and wraps the key.
4. **Upload to the node.** Chunks go to the **node**: `PUT {node_url}/v1/uploads/{ticket}/chunks/{n}`.
   - Chunks are 8 MiB, well under Cloudflare's 100 MB request limit.
   - Uploads are resumable. `GET .../status` lists the chunks received.
5. **Finish on the node.** `POST {node_url}/v1/uploads/{ticket}/complete` with the ciphertext SHA-256. The node checks the size and hash, stores the file and returns a **receipt**: upload id, ciphertext hash, size, signed with the node secret.
6. **Commit on main.** `POST /api/v1/uploads/{id}/complete` to **main**, sending:
   - the plaintext SHA-256 and size;
   - the wrapped key and key id;
   - the ciphertext SHA-256 and the node's receipt.

   Main checks the receipt and commits the file into the user's folders, with its content located on `node:<id>`. Folder sync, versions, tags and activity behave as for any other file.
7. **Background follow-up.** Thumbnail, media info and smart processing are queued. They read the file through the relay (below).

**Clients:**
- **Desktop client (vad-client):** gains the direct mode in its upload path. Older clients ignore the new fields, so main sends them `mode: "direct"` only when the client says it supports it; otherwise they upload to main and the file moves to the node afterwards.
- **Web uploads:** do the same with WebCrypto in a worker.

## Read flow (relay through main)

- **Where it applies:** every read of node-stored content uses one function on main: file content, video playback, downloads, zip downloads, public share links, the admin Full Library, thumbnails, frames and media probing, smart processing, the video editor and exports.
- **How a request is served:**
  1. It maps the requested byte range to VDR2 chunks.
  2. It fetches just those chunks: `GET {node_url}/v1/blobs/{id}` with `Range`, signed by main.
  3. It verifies and decrypts each chunk in memory and writes the plaintext range to the response.
  4. Seeking works because chunk positions are fixed.
- **Memory only:** a small in-memory chunk cache (default 64 MB, least-recently-used) smooths seeking and repeat reads. **Nothing is written to disk.**
- **FFmpeg:** it reads node content through a local loopback URL (`http://127.0.0.1:<port>/internal/relay/<one-time token>`) that serves the same relay with Range support. So probing, thumbnails, smart processing and exports need no temporary copy either.
- **Node unreachable:**
  - reads fail fast with "This file is on *node*, which is offline right now";
  - the UI shows an offline badge on affected files;
  - public share pages say the video is temporarily unavailable.

## Integrity and checks

- **At upload:** the node verifies the ciphertext hash, and main stores it.
- **Weekly (default):** a verify job has the node re-hash each stored file and compare it with main's record. Mismatches are flagged and repaired from a second copy if one exists.
- **On every read:** chunk tags (AES-GCM) catch tampering.
- **Plaintext check:** main checks the plaintext hash the first time it reads a whole file, for example during smart processing or a full download. An optional low-priority "deep verify" can read everything through the relay; it's off by default because of bandwidth.

## Deleting, trash and versions

- **Trash:** moving a file to trash keeps the node copy. Emptying the trash or the trash expiry queues a node delete job (existing job type).
- **Replaced files:** a replaced file's old version follows the version rules. It stays on the node until its version is purged.
- **Removing a node:** an admin must first "Move files back" to the main server (or to another node), or explicitly accept losing them.

## Failure cases

| Situation | Behaviour |
|---|---|
| Node offline at upload start | Main answers normal (non-direct) mode; the file stores on main and moves to the node when it's back (copy, verify, delete local) |
| Node goes offline mid-upload | The client retries chunks, then falls back to uploading to main from the last confirmed point |
| Node full | The same fallback as offline, plus an admin warning |
| Ticket expired or tampered | Node refuses; the client asks main for a fresh ticket |
| Node returns wrong or altered data | Chunk tags fail; read errors, check flagged; repair from a second copy if one exists |
| Node's disk lost | Files on it are lost unless a second copy exists (see Recommendations); main lists the affected files |
| Main restarts mid-move | Moves resume (existing copy machinery) |
| Node public URL changes | Admin edits it; the node also reports it in heartbeats |

## Security notes

- **Node endpoints:** only upload chunks or complete (with a valid ticket) and read or delete blobs (with main's signature). No listing, no names, no browsing.
- **Tickets:** single upload, size-capped, expiring, bound to user and node. Replay is refused once completed.
- **Main → node requests:** signed with the node secret (HMAC over method, path, time and nonce), and refused if older than 60 seconds.
- **Rate limits** on the node's public endpoints, plus the Cloudflare Access policy as an outer layer if the node owner wants one.
- **Logs:** no keys, tickets or content in logs on either side.
- **Server KEK:** the KEK is never used or copied. Node key material is sealed only with `node-copies.key`.

## Admin UI

- **Storage → Nodes:**
  - pair a remote node (code), public URL, status, version, disk use and free space;
  - assigned users, files and bytes stored;
  - traffic (uploaded to the node, relayed reads);
  - rotate key, move files back.
- **Users → edit:** upload origin selector, and "Move existing files" with progress.
- **Files:** a small "on *node*" indicator for files stored on a node, and an offline badge when it's unreachable.

## Data changes (migration 18)

- **`storage_nodes`:** `public_key`, `private_key_sealed`, `key_id`, `ticket_key_sealed`, `origin_enabled`.
- **`node_pairings`:** id, name, code hash, `public_url`, `expires_at`, `used_at`.
- **`users`:** `upload_origin_node_id` (null means the main server).
- **`remote_blobs`:** `user_id`, `sha256` (plaintext), `node_id`, `key_id`, `wrapped_key`, `cipher_sha256`, `cipher_size`, `state` (ok, moving, deleting, missing), `verified_at`.
- **`blobs`:** a node-held blob is recorded with a node marker instead of a local location id, so quota and dedup work unchanged.

## Phases

1. **Pairing, keys, VDR2 format, the node's inbound endpoint, and the relay read path.** Includes "Move existing files to the node", which proves the whole read side without any client change.
2. **Direct uploads from the web app.** Includes the fallback to main and the moving of fallback files.
3. **Direct uploads from the desktop client.** A vad-client change and release.
4. **Per-folder assignment, key rotation and re-wrap, deep verify, traffic stats.**

Each phase ships on its own and is usable.

## Testing

- **Unit:** VDR2 format (seek to any range, tampering, truncation, wrong key); ticket signing and expiry; HMAC request signing and replay; chunk-range mapping.
- **Integration (Docker, private network):** main plus a node, with network failures injected.
  - Pairing.
  - Direct upload, then commit, then playback with seeking.
  - Thumbnail, smart processing and a share link through the relay.
  - Fallback when the node is offline, then the move.
  - Node full; a tampered blob; trash and purge; moving files back.
- **Client:** desktop client direct upload against a test node; resume after an interrupted chunk.
- **Security:**
  - the node never receives the private key or content key (checked by inspecting everything sent to it);
  - other users' tickets are refused;
  - the node's endpoints expose no listing.

## Recommendations and open questions

1. **Second copy.** With an upload origin on a remote node, that node holds the only copy. Should a copy rule (to the main server, R2 or another node) be offered by default when assigning a user?
2. **Bandwidth.** Playback of node files uses the node's upload and the main server's download and upload. Is a per-node limit on concurrent relayed streams wanted?
3. **Folder-level assignment** is in phase 4. Is per-user enough to start?
4. **Existing users:** when a user is assigned, should their existing files move automatically, or only when the admin presses "Move existing files" (the current proposal)?
