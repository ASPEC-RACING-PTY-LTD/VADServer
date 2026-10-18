# Security

## Transport

- All remote traffic uses HTTPS (Let's Encrypt via Caddy, your proxy, a provided certificate, or a self-signed certificate that the desktop app pins after the user verifies its fingerprint). The desktop app refuses plain HTTP except for `localhost` and never disables certificate verification; with a pinned certificate any other certificate is rejected.
- HSTS is sent when HTTPS is detected; cookies are `HttpOnly`, `SameSite=Lax` and `Secure` over HTTPS.

## Web sessions

- Passwords are hashed with Argon2id (OWASP parameters), with bounded concurrency so login floods can't exhaust memory. Unknown users take the same time to reject as wrong passwords.
- Login is rate-limited per IP; repeated failures lock the account and the IP for 15 minutes. Failures are audit-logged.
- Session tokens are random 256-bit values; only their SHA-256 is stored. State-changing requests require a per-session CSRF token header.
- First-run setup requires the installer-generated setup token, so nobody else can claim a fresh server.

## Devices

1. The app generates an **Ed25519 key pair** locally and sends only the public key with the user's credentials (once, over TLS).
2. The request appears in the web interface with device name, OS, a privacy-preserving device identifier, key fingerprint, IP, approximate location and time, plus a verification code shown on both screens. It expires (default 15 minutes).
3. After approval, every device request is **signed** over the method, path, query, body SHA-256, device id, timestamp and a random nonce. The server rejects bad signatures, bodies that don't match the signed hash, timestamps more than 5 minutes off, reused nonces, and any signed request older than the server's start time (so a restart can't reopen the replay window).
4. The private key is stored with **Windows DPAPI** (bound to the Windows user); the password is never stored.
5. Revoking a device (user or admin) takes effect on its next request; its in-progress uploads are cancelled. Disabling an account blocks its sessions and devices.

## Isolation and validation

- Every file, upload, device and automation query is scoped to the authenticated user; foreign ids return 404.
- Names are validated against Windows and POSIX rules (no separators, `..`, control characters or reserved names). Disk paths are derived only from numeric user ids and verified SHA-256 hashes, so names can never influence filesystem paths.
- Storage locations can only be registered under `VAULTDOCK_STORAGE_ROOTS`.
- User content is served with `X-Content-Type-Options: nosniff`, a sandboxing CSP, and forced download for anything that could execute in the browser (HTML, SVG, XML, scripts). The web UI itself has a strict CSP.
- Request bodies are size-limited; JSON decoding rejects unknown fields.
- Automations cannot run code, shell commands or touch local files: actions only affect upload destinations, tags, folders in VaultDock, notifications and starting configured sync tasks. Templates are a fixed filter language. Desktop notifications use the WinRT API only (never a script-based fallback), so file names can't inject commands.

## Containers

Distroless static image, non-root user, read-only root filesystem, all capabilities dropped, `no-new-privileges`.

## The installer

`curl | bash` runs code before you can read it. The README shows how to download and inspect it first, or install from a clone. The installer only writes to its own directory and the storage path, and never deletes data without a typed confirmation.

## End-to-end encryption: evaluated, not enabled

Transport encryption (TLS) protects data in transit; files are stored unencrypted on your server's disks, which you control. Client-side (end-to-end) encryption would additionally protect against someone with access to the server's disks, but it would:

- remove server-side previews, video streaming with seeking, search inside metadata, and deduplication across devices;
- make account recovery impossible if keys are lost, and require key distribution between devices and the browser;
- complicate automations that need file metadata on the server.

For a self-hosted server the recommended approach is full-disk encryption (LUKS, BitLocker on a NAS, encrypted ZFS datasets) on the storage volumes. If end-to-end encryption is added later it should be an opt-in per folder using an established format (e.g. age or libsodium secretstream), not custom cryptography.

## Reporting

Please report vulnerabilities privately to the project maintainers (contact to be added before publication).
