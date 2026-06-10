package adaptive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Difficulty string

const (
	Easy   Difficulty = "easy"
	Medium Difficulty = "medium"
	Hard   Difficulty = "hard"
)

type TopicStats struct {
	Topic    string    `json:"topic"`
	Attempts int       `json:"attempts"`
	Correct  int       `json:"correct"`
	// last 10 answers, most recent last
	Recent   []bool    `json:"recent"`
	LastSeen time.Time `json:"last_seen"`
}

// Mastery blends overall accuracy (40%) with recent performance (60%).
// New topics start at 0.5 (neutral) so the first question is medium difficulty.
func (t *TopicStats) Mastery() float64 {
	if t.Attempts == 0 {
		return 0.5
	}
	overall := float64(t.Correct) / float64(t.Attempts)
	if len(t.Recent) == 0 {
		return overall
	}
	recentCorrect := 0
	for _, v := range t.Recent {
		if v {
			recentCorrect++
		}
	}
	recent := float64(recentCorrect) / float64(len(t.Recent))
	return 0.4*overall + 0.6*recent
}

func (t *TopicStats) Difficulty() Difficulty {
	m := t.Mastery()
	switch {
	case m < 0.4:
		return Easy
	case m < 0.75:
		return Medium
	default:
		return Hard
	}
}

func (t *TopicStats) record(correct bool) {
	t.Attempts++
	if correct {
		t.Correct++
	}
	t.Recent = append(t.Recent, correct)
	if len(t.Recent) > 10 {
		t.Recent = t.Recent[len(t.Recent)-10:]
	}
	t.LastSeen = time.Now()
}

type Engine struct {
	mu      sync.RWMutex
	topics  map[string]*TopicStats
	dataDir string
}

func New(dataDir string) (*Engine, error) {
	e := &Engine{
		topics:  make(map[string]*TopicStats),
		dataDir: dataDir,
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	_ = e.load() // ignore missing file on first run
	return e, nil
}

func (e *Engine) GetStats(topic string) TopicStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.topics[topic]; ok {
		return *s
	}
	return TopicStats{Topic: topic}
}

func (e *Engine) Record(topic string, correct bool) TopicStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.topics[topic]; !ok {
		e.topics[topic] = &TopicStats{Topic: topic}
	}
	e.topics[topic].record(correct)
	_ = e.save()
	return *e.topics[topic]
}

func (e *Engine) AllStats() []TopicStats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]TopicStats, 0, len(e.topics))
	for _, s := range e.topics {
		result = append(result, *s)
	}
	return result
}

// WeakAreas returns the n topics with lowest mastery.
func (e *Engine) WeakAreas(n int) []TopicStats {
	all := e.AllStats()
	sort.Slice(all, func(i, j int) bool {
		return all[i].Mastery() < all[j].Mastery()
	})
	if n > len(all) {
		n = len(all)
	}
	return all[:n]
}

func (e *Engine) save() error {
	data, err := json.MarshalIndent(e.topics, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(e.dataDir, "progress.json"), data, 0644)
}

func (e *Engine) load() error {
	data, err := os.ReadFile(filepath.Join(e.dataDir, "progress.json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &e.topics)
}
