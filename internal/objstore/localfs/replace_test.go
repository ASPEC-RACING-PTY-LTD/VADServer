package localfs

import (
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore/objstoretest"
)

func TestReplacerConformance(t *testing.T) {
	objstoretest.RunReplacer(t, func(t *testing.T) objstoretest.ReplaceableStore { return newStore(t) })
}
