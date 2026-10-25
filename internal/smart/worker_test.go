package smart

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWorkerAudioIsSized: audio chunks are sent with their length (the
// worker cannot read a streamed body of unknown size).
func TestWorkerAudioIsSized(t *testing.T) {
	var got int64 = -1
	var te []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, te = r.ContentLength, r.TransferEncoding
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"vad":[]}`))
	}))
	defer srv.Close()
	n := int64(SampleRate * 2)
	body := io.MultiReader(bytes.NewReader(WAVHeader(int(n))), bytes.NewReader(make([]byte, n)))
	if _, err := NewWorker(srv.URL, "").Audio(context.Background(), body, 44+n, []string{"vad"}, ""); err != nil {
		t.Fatal(err)
	}
	if got != 44+n || len(te) != 0 {
		t.Fatalf("content length %d, transfer encoding %v", got, te)
	}
}

// TestRealWorkerRoundTrip runs against a real worker when
// SMART_TEST_WORKER_URL is set (voice activity needs no download; sound
// events run when the ast model is installed).
func TestRealWorkerRoundTrip(t *testing.T) {
	u := os.Getenv("SMART_TEST_WORKER_URL")
	if u == "" {
		t.Skip("SMART_TEST_WORKER_URL not set")
	}
	w := NewWorker(u, os.Getenv("SMART_TEST_WORKER_TOKEN"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := w.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tasks := []string{"vad"}
	if st.Installed()["ast"] {
		tasks = append(tasks, "events")
	}
	// 3 s silence, 4 s tone, 3 s silence: streamed exactly as the analyzer does.
	var s []float64
	s = append(s, quiet(3)...)
	s = append(s, tone(4, 1000, 0.5)...)
	s = append(s, quiet(3)...)
	p := pcm(s)
	n := int64(len(p))
	body := io.MultiReader(bytes.NewReader(WAVHeader(int(n))), bytes.NewReader(p))
	res, err := w.Audio(ctx, body, 44+n, tasks, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(tasks, ","), "events") {
		if res.Events == nil || len(res.Events.Rows) != 10 || len(res.Events.Labels) != 527 {
			t.Fatalf("events: %+v", res.Events)
		}
		sine := res.Events.Column("Sine wave")
		if sine[0] != 0 || sine[5] < 0.2 || math.IsNaN(sine[5]) {
			t.Fatalf("sine column %v", sine)
		}
	}
	t.Logf("tasks %v ok, vad %v", tasks, res.VAD)
}
