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
	mux        *http.ServeMux
	ollama     *ollama.Client
	adaptive   *adaptive.Engine
	sessions   map[string]*quizSession
	sessLock   sync.Mutex
	exSessions map[string]*exerciseSession
	exLock     sync.Mutex
	lcd        lcdStore
}

type lcdStore struct {
	mu      sync.Mutex
	line1   string
	line2   string
	expires time.Time
}

type exerciseSession struct {
	Topic        string
	Language     string
	Task         string
	Requirements []string
	CreatedAt    time.Time
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
		sessions:   make(map[string]*quizSession),
		exSessions: make(map[string]*exerciseSession),
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
	s.mux.HandleFunc("POST /api/lcd", s.handleLCDSet)
	s.mux.HandleFunc("GET /api/lcd", s.handleLCDGet)
	s.mux.HandleFunc("POST /api/ask", s.handleAsk)
	s.mux.HandleFunc("POST /api/exercise/start", s.handleExerciseStart)
	s.mux.HandleFunc("POST /api/exercise/submit", s.handleExerciseSubmit)
	s.mux.HandleFunc("POST /api/flashcards", s.handleFlashcards)
	// catch-all: return JSON 404 instead of Go's default plain-text "404 page not found"
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpErr(w, http.StatusNotFound, "unknown endpoint: "+r.Method+" "+r.URL.Path)
	})
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

func (s *Server) handleExerciseStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic    string `json:"topic"`
		Context  string `json:"context"`
		Language string `json:"language"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Topic == "" {
		httpErr(w, http.StatusBadRequest, "topic required")
		return
	}
	if req.Language == "" {
		req.Language = "go"
	}

	system := `You are a coding teacher. Generate a practical coding exercise. Reply ONLY with JSON:
{"task":"clear problem statement","requirements":["req1","req2"],"hints":["hint1"]}`
	user := fmt.Sprintf("Topic: %s\nLanguage: %s", req.Topic, req.Language)
	if req.Context != "" {
		user += "\nContext: " + req.Context
	}

	result, err := s.ollama.Chat(r.Context(), system, user)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}

	var task struct {
		Task         string   `json:"task"`
		Requirements []string `json:"requirements"`
		Hints        []string `json:"hints"`
	}
	taskText := result
	if err := parseJSON(result, &task); err == nil && task.Task != "" {
		taskText = task.Task
	}

	id := newID()
	s.exLock.Lock()
	s.exSessions[id] = &exerciseSession{
		Topic:        req.Topic,
		Language:     req.Language,
		Task:         taskText,
		Requirements: task.Requirements,
		CreatedAt:    time.Now(),
	}
	s.exLock.Unlock()

	jsonResp(w, http.StatusOK, map[string]any{
		"session_id":   id,
		"task":         taskText,
		"requirements": task.Requirements,
		"hints":        task.Hints,
		"language":     req.Language,
	})
}

func (s *Server) handleExerciseSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Code      string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" {
		httpErr(w, http.StatusBadRequest, "session_id and code required")
		return
	}

	s.exLock.Lock()
	sess, ok := s.exSessions[req.SessionID]
	s.exLock.Unlock()
	if !ok {
		httpErr(w, http.StatusNotFound, "session not found")
		return
	}

	system := `Evaluate the submitted code for the exercise. Reply ONLY with JSON:
{"passed":bool,"score":float,"feedback":"string","issues":["string"],"improvements":["string"]}`
	user := fmt.Sprintf("Exercise: %s\nLanguage: %s\nCode:\n```%s\n%s\n```",
		sess.Task, sess.Language, sess.Language, req.Code)

	result, err := s.ollama.Chat(r.Context(), system, user)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}

	var eval struct {
		Passed       bool     `json:"passed"`
		Score        float64  `json:"score"`
		Feedback     string   `json:"feedback"`
		Issues       []string `json:"issues"`
		Improvements []string `json:"improvements"`
	}
	if err := parseJSON(result, &eval); err != nil {
		eval.Feedback = result
	}

	stats := s.adaptive.Record(sess.Topic, eval.Passed)
	if eval.Passed {
		s.exLock.Lock()
		delete(s.exSessions, req.SessionID)
		s.exLock.Unlock()
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"passed":       eval.Passed,
		"score":        eval.Score,
		"feedback":     eval.Feedback,
		"issues":       eval.Issues,
		"improvements": eval.Improvements,
		"new_mastery":  round2(stats.Mastery()),
	})
}

func (s *Server) handleFlashcards(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Topic   string `json:"topic"`
		Content string `json:"content"`
		Count   int    `json:"count"`
		Deck    string `json:"deck"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Topic == "" {
		httpErr(w, http.StatusBadRequest, "topic required")
		return
	}
	if req.Count <= 0 || req.Count > 20 {
		req.Count = 5
	}
	if req.Deck == "" {
		req.Deck = req.Topic
	}

	system := fmt.Sprintf(`Generate %d flashcards for "%s". Reply ONLY with JSON:
{"cards":[{"front":"concise question","back":"answer with bullet points where helpful","tags":["tag1"]}]}`, req.Count, req.Topic)
	user := req.Topic
	if req.Content != "" {
		user += "\nContext:\n" + req.Content
	}

	result, err := s.ollama.Chat(r.Context(), system, user)
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}

	var parsed struct {
		Cards []struct {
			Front string   `json:"front"`
			Back  string   `json:"back"`
			Tags  []string `json:"tags"`
		} `json:"cards"`
	}
	if err := parseJSON(result, &parsed); err != nil || len(parsed.Cards) == 0 {
		httpErr(w, http.StatusInternalServerError, "failed to generate cards")
		return
	}

	// Build Obsidian-to-Anki markdown matching vault format
	deckLower := strings.ToLower(strings.ReplaceAll(req.Deck, " ", "-"))
	topicSlug := strings.ToLower(strings.ReplaceAll(req.Topic, " ", "-"))
	var sb strings.Builder
	fmt.Fprintf(&sb, "---\ntags: [flashcards/%s, review/daily]\nanki-deck: %s\n---\n\nTARGET DECK: %s\n\n", deckLower, req.Deck, req.Deck)
	for _, c := range parsed.Cards {
		tagParts := make([]string, 0, len(c.Tags)+1)
		tagParts = append(tagParts, "#"+topicSlug)
		for _, t := range c.Tags {
			tagParts = append(tagParts, "#"+strings.ToLower(strings.ReplaceAll(t, " ", "-")))
		}
		fmt.Fprintf(&sb, "START\nBasic\n%s\nBack:\n%s\nTags: %s\n<!--ID: 0-->\nEND\n\n---\n",
			c.Front, c.Back, strings.Join(tagParts, " "))
	}

	type cardOut struct {
		Front string `json:"front"`
		Back  string `json:"back"`
	}
	out := make([]cardOut, len(parsed.Cards))
	for i, c := range parsed.Cards {
		out[i] = cardOut{Front: c.Front, Back: c.Back}
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"cards":    out,
		"markdown": sb.String(),
		"topic":    req.Topic,
		"deck":     req.Deck,
	})
}

func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt   string `json:"prompt"`
		Context  string `json:"context"`
		Filetype string `json:"filetype"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Prompt == "" {
		httpErr(w, http.StatusBadRequest, "prompt required")
		return
	}

	system := `You are Diamond, a direct and concise coding assistant. Answer clearly, no filler.`
	user := req.Prompt
	if req.Context != "" {
		ft := req.Filetype
		if ft == "" {
			ft = "code"
		}
		user += "\n\n```" + ft + "\n" + req.Context + "\n```"
	}

	result, err := s.ollama.Chat(r.Context(), system, user)
	if err != nil {
		log.Printf("ask: %v", err)
		httpErr(w, http.StatusServiceUnavailable, "LLM unavailable")
		return
	}

	jsonResp(w, http.StatusOK, map[string]any{"response": result, "prompt": req.Prompt})
}

func (s *Server) handleLCDSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Line1 string `json:"line1"`
		Line2 string `json:"line2"`
		TTL   int    `json:"ttl"` // seconds; default 30
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Line1 == "" {
		httpErr(w, http.StatusBadRequest, "line1 required")
		return
	}
	if req.TTL <= 0 {
		req.TTL = 30
	}
	// Truncate to 16 chars (standard LCD width)
	if len(req.Line1) > 16 {
		req.Line1 = req.Line1[:16]
	}
	if len(req.Line2) > 16 {
		req.Line2 = req.Line2[:16]
	}

	s.lcd.mu.Lock()
	s.lcd.line1 = req.Line1
	s.lcd.line2 = req.Line2
	s.lcd.expires = time.Now().Add(time.Duration(req.TTL) * time.Second)
	s.lcd.mu.Unlock()

	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "ttl": req.TTL})
}

func (s *Server) handleLCDGet(w http.ResponseWriter, r *http.Request) {
	s.lcd.mu.Lock()
	line1 := s.lcd.line1
	line2 := s.lcd.line2
	active := line1 != "" && time.Now().Before(s.lcd.expires)
	s.lcd.mu.Unlock()

	jsonResp(w, http.StatusOK, map[string]any{
		"active": active,
		"line1":  line1,
		"line2":  line2,
	})
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
