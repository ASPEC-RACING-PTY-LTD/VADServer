package smart

// Anonymous speaker numbering: voice embeddings of the utterances are
// clustered by similarity. Nobody is identified; speakers are just
// "speaker 1", "speaker 2" within one recording.

const (
	sameSpeaker  = 0.5  // cosine above which an utterance joins a voice
	mergeSpeaker = 0.58 // voices this similar are the same person
)

// ClusterSpeakers assigns Speaker numbers (1-based, by first appearance)
// to utterances from their voice embeddings (nil rows: too short to tell,
// they keep 0 or take their neighbour's voice). It returns the number of
// voices.
func ClusterSpeakers(u []Utterance, emb [][]float32) int {
	type cluster struct {
		sum []float64
		n   int
		dur float64
	}
	var cs []*cluster
	label := make([]int, len(u))
	centroid := func(c *cluster) []float32 {
		v := make([]float32, len(c.sum))
		for i, s := range c.sum {
			v[i] = float32(s)
		}
		Normalize(v)
		return v
	}
	for i := range u {
		label[i] = -1
		if i >= len(emb) || len(emb[i]) == 0 {
			continue
		}
		best, bestSim := -1, -1.0
		for k, c := range cs {
			if s := Dot(centroid(c), emb[i]); s > bestSim {
				best, bestSim = k, s
			}
		}
		if best < 0 || bestSim < sameSpeaker {
			cs = append(cs, &cluster{sum: make([]float64, len(emb[i]))})
			best = len(cs) - 1
		}
		c := cs[best]
		for k, f := range emb[i] {
			c.sum[k] += float64(f)
		}
		c.n++
		c.dur += u[i].End - u[i].Start
		label[i] = best
	}
	// Merge voices that turned out alike; fold tiny ones into the nearest.
	parent := make([]int, len(cs))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			i = parent[i]
		}
		return i
	}
	for a := range cs {
		for b := a + 1; b < len(cs); b++ {
			if Dot(centroid(cs[a]), centroid(cs[b])) >= mergeSpeaker {
				parent[find(b)] = find(a)
			}
		}
	}
	for a, c := range cs {
		if c.dur >= 1.5 || len(cs) < 2 {
			continue
		}
		best, bestSim := -1, -1.0
		for b, o := range cs {
			if b != a && o.dur >= 1.5 {
				if s := Dot(centroid(c), centroid(o)); s > bestSim {
					best, bestSim = b, s
				}
			}
		}
		if best >= 0 {
			parent[find(a)] = find(best)
		}
	}
	number := map[int]int{}
	for i := range u {
		if label[i] < 0 {
			continue
		}
		root := find(label[i])
		if _, ok := number[root]; !ok {
			number[root] = len(number) + 1
		}
		u[i].Speaker = number[root]
	}
	// Utterances too short to embed take the surrounding voice.
	for i := range u {
		if label[i] < 0 {
			if i > 0 {
				u[i].Speaker = u[i-1].Speaker
			} else if i+1 < len(u) && label[i+1] >= 0 {
				u[i].Speaker = number[find(label[i+1])]
			}
		}
	}
	return len(number)
}
