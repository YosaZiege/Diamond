package server

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yosa/diamond/internal/adaptive"
	"github.com/yosa/diamond/internal/ollama"
)

type Config struct {
	OllamaURL string
	Model     string
	Port      string
	DataDir   string
}

type Server struct {
	mux      *http.ServeMux
	ollama   *ollama.Client
	adaptive *adaptive.Engine
	sessions map[string]*quizSession
	sessLock sync.Mutex
}

type quizSession struct {
	Topic      string
	Difficulty adaptive.Difficulty
	Answered   []bool
	Current    string
	CreatedAt  time.Time
}

func New(cfg Config) (*Server, error) {
	eng, err := adaptive.New(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("adaptive engine: %w", err)
	}
	s := &Server{
		mux:      http.NewServeMux(),
		ollama:   ollama.New(cfg.OllamaURL, cfg.Model),
		adaptive: eng,
		sessions: make(map[string]*quizSession),
	}
	s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/explain", s.handleExplain)
	s.mux.HandleFunc("POST /api/evaluate", s.handleEvaluate)
	s.mux.HandleFunc("POST /api/quiz/start", s.handleQuizStart)
	s.mux.HandleFunc("POST /api/quiz/answer", s.handleQuizAnswer)
	s.mux.HandleFunc("GET /api/progress", s.handleProgress)
	s.mux.HandleFunc("GET /api/weak-areas", s.handleWeakAreas)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ollamaStatus := "ok"
	if err := s.ollama.Ping(r.Context()); err != nil {
		ollamaStatus = err.Error()
	}
	jsonResp(w, http.StatusOK, map[string]string{
		"status": "ok",
		"ollama": ollamaStatus,
	})
}

func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic   string `json:"topic"`
		Context string `json:"context"`
		Level   string `json:"level"` // easy | medium | hard; auto-detected if empty
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Topic == "" {
		httpErr(w, http.StatusBadRequest, "topic required")
		return
	}

	stats := s.adaptive.GetStats(req.Topic)
	level := req.Level
	if level == "" {
		level = string(stats.Difficulty())
	}

	system := `You are Diamond, a concise technical tutor. Explain the concept clearly, then give 1-2 concrete examples. No fluff.`
	user := fmt.Sprintf("Explain: %s\nLevel: %s", req.Topic, level)
	if req.Context != "" {
		user += "\nCode context:\n```\n" + req.Context + "\n```"
	}

	result, err := s.ollama.Chat(r.Context(), system, user)
	if err != nil {
		log.Printf("explain %q: %v", req.Topic, err)
		httpErr(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"explanation": result,
		"topic":       req.Topic,
		"level":       level,
		"mastery":     round2(stats.Mastery()),
	})
}

func (s *Server) handleEvaluate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic    string `json:"topic"`
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	system := `Evaluate the learner's answer. Reply ONLY with valid JSON, no markdown fences:
{"correct":bool,"score":float,"feedback":"string","key_points":["string"]}`
	user := fmt.Sprintf("Topic: %s\nQuestion: %s\nAnswer: %s", req.Topic, req.Question, req.Answer)

	result, err := s.ollama.Chat(r.Context(), system, user)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}

	var eval struct {
		Correct   bool     `json:"correct"`
		Score     float64  `json:"score"`
		Feedback  string   `json:"feedback"`
		KeyPoints []string `json:"key_points"`
	}
	if err := parseJSON(result, &eval); err != nil {
		// LLM didn't return clean JSON — return raw feedback
		jsonResp(w, http.StatusOK, map[string]any{"feedback": result, "raw": true})
		return
	}

	stats := s.adaptive.Record(req.Topic, eval.Correct)
	jsonResp(w, http.StatusOK, map[string]any{
		"correct":     eval.Correct,
		"score":       eval.Score,
		"feedback":    eval.Feedback,
		"key_points":  eval.KeyPoints,
		"new_mastery": round2(stats.Mastery()),
		"difficulty":  string(stats.Difficulty()),
	})
}

func (s *Server) handleQuizStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic string `json:"topic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Topic == "" {
		httpErr(w, http.StatusBadRequest, "topic required")
		return
	}

	stats := s.adaptive.GetStats(req.Topic)
	diff := stats.Difficulty()

	system := `Generate a single quiz question. Reply ONLY with valid JSON, no markdown:
{"question":"string","hint":"short optional hint or empty string"}`
	user := fmt.Sprintf("Topic: %s\nDifficulty: %s\nMastery: %.0f%%", req.Topic, diff, stats.Mastery()*100)

	result, err := s.ollama.Chat(r.Context(), system, user)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}

	var qData struct {
		Question string `json:"question"`
		Hint     string `json:"hint"`
	}
	question := result
	if err := parseJSON(result, &qData); err == nil && qData.Question != "" {
		question = qData.Question
		if qData.Hint != "" {
			question += "\n\nHint: " + qData.Hint
		}
	}

	id := newID()
	s.sessLock.Lock()
	s.sessions[id] = &quizSession{
		Topic:      req.Topic,
		Difficulty: diff,
		Current:    question,
		CreatedAt:  time.Now(),
	}
	s.sessLock.Unlock()

	jsonResp(w, http.StatusOK, map[string]any{
		"session_id": id,
		"question":   question,
		"difficulty": string(diff),
		"mastery":    round2(stats.Mastery()),
	})
}

func (s *Server) handleQuizAnswer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Answer    string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid request")
		return
	}

	s.sessLock.Lock()
	sess, ok := s.sessions[req.SessionID]
	s.sessLock.Unlock()
	if !ok {
		httpErr(w, http.StatusNotFound, "session not found")
		return
	}

	system := `Evaluate a quiz answer. Reply ONLY with valid JSON, no markdown:
{"correct":bool,"feedback":"string","score":float}`
	user := fmt.Sprintf("Topic: %s\nQuestion: %s\nAnswer: %s", sess.Topic, sess.Current, req.Answer)

	result, err := s.ollama.Chat(r.Context(), system, user)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}

	var eval struct {
		Correct  bool    `json:"correct"`
		Feedback string  `json:"feedback"`
		Score    float64 `json:"score"`
	}
	if err := parseJSON(result, &eval); err != nil {
		eval.Feedback = result
	}

	stats := s.adaptive.Record(sess.Topic, eval.Correct)
	sess.Answered = append(sess.Answered, eval.Correct)

	// Hard cap at 10 questions per session
	done := len(sess.Answered) >= 10
	var nextQuestion string

	if !done {
		sys2 := `Generate the next quiz question based on performance. Reply ONLY with valid JSON:
{"question":"string","done":bool}
Set done:true if 5+ questions answered and mastery>75%.`
		usr2 := fmt.Sprintf("Topic: %s\nDifficulty: %s\nAnswered: %d\nMastery: %.0f%%",
			sess.Topic, stats.Difficulty(), len(sess.Answered), stats.Mastery()*100)

		if r2, err2 := s.ollama.Chat(r.Context(), sys2, usr2); err2 == nil {
			var next struct {
				Question string `json:"question"`
				Done     bool   `json:"done"`
			}
			if err2 := parseJSON(r2, &next); err2 == nil {
				done = done || next.Done
				nextQuestion = next.Question
			}
		}
	}

	if done {
		s.sessLock.Lock()
		delete(s.sessions, req.SessionID)
		s.sessLock.Unlock()
	} else if nextQuestion != "" {
		s.sessLock.Lock()
		sess.Current = nextQuestion
		sess.Difficulty = stats.Difficulty()
		s.sessLock.Unlock()
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"correct":       eval.Correct,
		"feedback":      eval.Feedback,
		"score":         eval.Score,
		"new_mastery":   round2(stats.Mastery()),
		"difficulty":    string(stats.Difficulty()),
		"done":          done,
		"next_question": nextQuestion,
	})
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	all := s.adaptive.AllStats()
	type item struct {
		Topic      string  `json:"topic"`
		Mastery    float64 `json:"mastery"`
		Attempts   int     `json:"attempts"`
		Difficulty string  `json:"difficulty"`
	}
	items := make([]item, len(all))
	for i, t := range all {
		items[i] = item{
			Topic:      t.Topic,
			Mastery:    round2(t.Mastery()),
			Attempts:   t.Attempts,
			Difficulty: string(t.Difficulty()),
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{"topics": items})
}

func (s *Server) handleWeakAreas(w http.ResponseWriter, r *http.Request) {
	weak := s.adaptive.WeakAreas(5)
	type item struct {
		Topic      string  `json:"topic"`
		Mastery    float64 `json:"mastery"`
		Difficulty string  `json:"difficulty"`
	}
	items := make([]item, len(weak))
	for i, t := range weak {
		items[i] = item{
			Topic:      t.Topic,
			Mastery:    round2(t.Mastery()),
			Difficulty: string(t.Difficulty()),
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{"weak_areas": items})
}

// ---- helpers ----

func jsonResp(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func httpErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// parseJSON extracts JSON from an LLM response that may have prose around it.
func parseJSON(s string, v any) error {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end <= start {
		return fmt.Errorf("no JSON in response")
	}
	return json.Unmarshal([]byte(s[start:end+1]), v)
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
