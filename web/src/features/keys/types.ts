// Hand-written mirror of wire.ServerIdentity (sdk/wire/keys.go, api-v1 §5A).

export interface ServerIdentity {
  sik_pub: string
  /** Uppercase hex of the first 16 bytes of SHA-256(sik_pub), 8 groups of 4 joined by '-' (§14 Rev 1.1b). */
  sik_fingerprint: string
  sek_pub: string
  sek_version: number
  sek_sig: string
}

/** GET /keys/server. */
export interface ServerIdentityResponse {
  server_identity: ServerIdentity
}

/** Key status as the UI shows it: available, or unavailable on `409 escrow_unavailable`. */
export type ServerKeyStatus = { state: 'available'; identity: ServerIdentity } | { state: 'unavailable'; message: string }

/** Normalises a SIK fingerprint for comparison: case-insensitive, ignoring ':', '-' and spaces (§14 Rev 1.1b). */
export function normalizeFingerprint(fp: string): string {
  return fp.replace(/[:\-\s]/g, '').toUpperCase()
}

export function sameFingerprint(a: string, b: string): boolean {
  return normalizeFingerprint(a) === normalizeFingerprint(b)
}
