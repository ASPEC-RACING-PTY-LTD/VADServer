package api

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Finishing an upload used to read the whole assembled file back to hash it,
// which on a slow disk took longer than receiving it. The file's SHA-256 is
// now computed while it arrives: whenever the chunks from the start of the
// file are complete up to some point, they are fed to the hash in order
// (read back right after they were written, so from the page cache).
// Finishing then only hashes what is left (usually nothing). The state lives
// in memory; after a restart the next finish hashes the file from disk.

type streamHash struct {
	mu      sync.Mutex
	h       hash.Hash
	next    int64  // first chunk not yet hashed
	have    []byte // received bitmap as known here
	running bool
	dirty   bool
	broken  bool // a hashed chunk was written again: start over at finish
	used    time.Time
}

// streamHashHits counts finishes that used the in-flight hash (tests).
var streamHashHits atomic.Int64

var streamHashes = struct {
	sync.Mutex
	m map[string]*streamHash
}{m: map[string]*streamHash{}}

func streamHashFor(id string, create bool) *streamHash {
	streamHashes.Lock()
	defer streamHashes.Unlock()
	sh := streamHashes.m[id]
	if sh == nil && create {
		sh = &streamHash{h: sha256.New()}
		streamHashes.m[id] = sh
	}
	if sh != nil {
		sh.used = time.Now()
	}
	// Forget sessions nobody wrote to for a day (expired or abandoned).
	if len(streamHashes.m) > 64 {
		for k, v := range streamHashes.m {
			if time.Since(v.used) > 24*time.Hour {
				delete(streamHashes.m, k)
			}
		}
	}
	return sh
}

func dropStreamHash(id string) {
	streamHashes.Lock()
	delete(streamHashes.m, id)
	streamHashes.Unlock()
}

// chunkStored records that a chunk of up (received bitmap bm, including the
// chunk) is on disk and hashes what has become contiguous. rewrite is true
// when the chunk had been stored before.
func chunkStored(up *store.Upload, tmp string, bm []byte, rewrite bool) {
	sh := streamHashFor(up.ID, true)
	sh.mu.Lock()
	if len(sh.have) < len(bm) {
		sh.have = append(sh.have, make([]byte, len(bm)-len(sh.have))...)
	}
	for i := range bm {
		sh.have[i] |= bm[i]
	}
	if rewrite {
		// The region may change under a hash pass; finishing hashes the file.
		sh.broken = true
	}
	if sh.running {
		sh.dirty = true
		sh.mu.Unlock()
		return
	}
	sh.running = true
	sh.mu.Unlock()
	// In the request, so the file is closed again before it answers; a
	// parallel chunk of the same file only marks more work for this pass.
	sh.advance(up, tmp)
}

func (sh *streamHash) advance(up *store.Upload, tmp string) {
	f, err := os.Open(tmp)
	sh.mu.Lock()
	defer func() {
		sh.running = false
		sh.mu.Unlock()
		if f != nil {
			f.Close()
		}
	}()
	if err != nil {
		sh.broken = true
		return
	}
	buf := make([]byte, 256<<10)
	for {
		sh.dirty = false
		for !sh.broken && sh.next < up.ChunksTotal && sh.have[sh.next/8]&(1<<(sh.next%8)) != 0 {
			idx := sh.next
			sh.mu.Unlock()
			_, err := io.CopyBuffer(sh.h, io.NewSectionReader(f, idx*up.ChunkSize, up.ChunkLen(idx)), buf)
			sh.mu.Lock()
			if err != nil {
				sh.broken = true
				break
			}
			sh.next++
		}
		if !sh.dirty {
			return
		}
	}
}

// fileSHA256 returns the SHA-256 of the assembled upload, continuing the
// hash computed while it arrived and reading only the chunks it has not
// covered yet. The upload is finalizing, so no chunk is written meanwhile.
func fileSHA256(up *store.Upload, tmp string) (string, error) {
	sh := streamHashFor(up.ID, false)
	if sh != nil {
		// Wait for a running pass to settle.
		for {
			sh.mu.Lock()
			if !sh.running {
				break
			}
			sh.mu.Unlock()
			time.Sleep(5 * time.Millisecond)
		}
		defer sh.mu.Unlock()
		if !sh.broken {
			f, err := os.Open(tmp)
			if err != nil {
				return "", err
			}
			defer f.Close()
			if _, err := io.CopyBuffer(sh.h, io.NewSectionReader(f, sh.next*up.ChunkSize, max(0, up.Size-sh.next*up.ChunkSize)), make([]byte, 1<<20)); err != nil {
				return "", err
			}
			sh.next = up.ChunksTotal
			sum := hex.EncodeToString(sh.h.Sum(nil))
			sh.broken = true // used once
			streamHashHits.Add(1)
			return sum, nil
		}
	}
	return storage.HashFile(tmp)
}
