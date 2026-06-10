# DIAMOND — Project Bible

> **Local-first. Adaptive. Invisible.**  
> An LLM-powered learning layer that lives beside your editor and vault.

---

## Philosophy

Diamond runs on **your** hardware. No cloud subscriptions, no data sent to third parties, no internet required mid-session. Your Obsidian vault is the knowledge store; your Neovim is the cockpit; the external machine is the brain. Diamond is invisible when you don't need it, and immediately useful when you do.

Three principles:

| Principle | Meaning |
|-----------|---------|
| **Local-first** | LLM inference happens on the external machine via Ollama. Nothing leaves your network. |
| **Adaptive** | Diamond tracks mastery per topic and automatically adjusts question difficulty. |
| **Invisible** | No UI to open. A keymap in Neovim, a float window with the answer, then back to work. |

---

## Network Architecture

```
┌─────────────────────────────┐        LAN / Tailscale
│   Main Machine              │ ─────────────────────────► ┌──────────────────────────┐
│   (Neovim + Obsidian)       │                             │  External Machine (8GB)  │
│                             │  HTTP :7331                 │                          │
│   neovim/diamond.lua        │ ◄──────────────────────────►│  diamond (Go server)     │
│   Obsidian REST plugin      │                             │  │                        │
│                             │  UFW: only main IP allowed  │  └── Ollama :11434        │
└─────────────────────────────┘                             │      llama3.2:3b          │
                                                            │      qwen2.5-coder:3b     │
                                                            └──────────────────────────┘
```

---

## File Responsibilities

| File | Runs on | Purpose |
|------|---------|---------|
| `main.go` | External machine | Entry point; reads env vars, starts HTTP server |
| `internal/ollama/client.go` | External machine | Thin Ollama `/api/chat` client (stdlib only) |
| `internal/adaptive/engine.go` | External machine | Per-topic mastery tracking; persists to `progress.json` |
| `internal/server/server.go` | External machine | HTTP handlers for all Diamond API endpoints |
| `systemd/diamond.service` | External machine | Runs Diamond on boot, restarts on failure |
| `scripts/setup.sh` | External machine | One-shot bootstrap (Go, Ollama, models, systemd, UFW) |
| `neovim/diamond.lua` | Main machine | Neovim plugin; async `curl` → float window |
| `DIAMOND.md` | Vault root | This document |

---

## Vault Convention

Place this file at the root of your Obsidian vault:

```
YourVault/
├── DIAMOND.md            ← this file
├── 01_Areas/
│   ├── Go/
│   ├── Algorithms/
│   ├── Systems/
│   └── Math/
├── 02_Resources/
├── 03_Projects/
└── 04_Archive/
```

Topic names you pass to Diamond (e.g. `DiamondQuiz Go channels`) should mirror the folder names under `01_Areas/`. This keeps your notes and learning progress aligned.

---

## External Machine Setup

### Requirements
- Debian/Ubuntu-based Linux (or adapt for Arch/Fedora)
- 8GB RAM minimum
- Git, `curl` available

### Steps

```bash
# 1. Clone the repo
git clone https://github.com/yosa/diamond.git
cd diamond

# 2. Run setup (pass your main machine's IP to lock down the firewall)
chmod +x scripts/setup.sh
./scripts/setup.sh 192.168.1.50   # replace with your main machine IP

# 3. Verify
curl http://localhost:7331/api/health
```

### Recommended Models for 8GB RAM

| Model | Size | Best for |
|-------|------|----------|
| `llama3.2:3b` | ~2GB | General explanations, quizzes, chat |
| `qwen2.5-coder:3b` | ~2GB | Code explanation, evaluation |
| `phi3.5:mini` | ~2.2GB | Fast answers, lower quality |

Default is `llama3.2:3b`. Override with:
```bash
DIAMOND_MODEL=qwen2.5-coder:3b systemctl restart diamond
# or in /etc/systemd/system/diamond.service
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DIAMOND_OLLAMA_URL` | `http://localhost:11434` | Ollama endpoint |
| `DIAMOND_MODEL` | `llama3.2:3b` | Model to use |
| `DIAMOND_PORT` | `7331` | Diamond HTTP port |
| `DIAMOND_DATA_DIR` | `/var/lib/diamond` | Progress JSON location |

---

## Main Machine Setup (Neovim)

Add to your Neovim config (lazy.nvim example):

```lua
{
  dir = '~/Projects/diamond/neovim',  -- or wherever you cloned it
  name = 'diamond',
  config = function()
    require('diamond').setup({
      server = 'http://192.168.1.100:7331',  -- your external machine's IP
    })
  end,
}
```

Or manually in `init.lua`:
```lua
vim.opt.rtp:prepend('~/Projects/diamond/neovim')
require('diamond').setup({ server = 'http://192.168.1.100:7331' })
```

---

## Keymap Reference

| Keymap | Command | API Endpoint | Description |
|--------|---------|--------------|-------------|
| `<leader>de` | `:DiamondExplain` | `POST /api/explain` | Explain word under cursor or visual selection |
| `<leader>dq` | `:DiamondQuiz [topic]` | `POST /api/quiz/start` | Start an adaptive quiz on a topic |
| `<leader>dp` | `:DiamondProgress` | `GET /api/progress` | Show mastery across all topics |
| `<leader>dw` | `:DiamondWeakAreas` | `GET /api/weak-areas` | Show 5 weakest topics |
| `<leader>dh` | `:DiamondHealth` | `GET /api/health` | Ping server and Ollama |
| — | `:DiamondAnswer <ans>` | `POST /api/quiz/answer` | Submit answer to active quiz question |
| — | `:DiamondEvaluate` | `POST /api/evaluate` | Evaluate a free-form answer |

All floats close with `q` or `<Esc>`.

---

## Adaptive Engine

Diamond tracks per-topic stats in `/var/lib/diamond/progress.json`.

**Mastery score** (0.0–1.0):
```
mastery = 0.4 × (all_correct / all_attempts) + 0.6 × (recent_correct / recent_10)
```
Recent answers are weighted more heavily so improvement (or regression) is reflected quickly.

**Difficulty mapping:**

| Mastery | Difficulty | Questions are... |
|---------|------------|-----------------|
| < 40% | `easy` | Definitions, basic usage |
| 40–75% | `medium` | Application, comparison |
| > 75% | `hard` | Edge cases, design tradeoffs |

**Weak areas** = topics sorted by mastery ascending (lowest first, top 5 shown).

New topics start at 0.5 mastery → `medium` difficulty until proven otherwise.

---

## API Reference

### `GET /api/health`
```json
{ "status": "ok", "ollama": "ok" }
```

### `POST /api/explain`
```json
// Request
{ "topic": "Go channels", "context": "optional code", "level": "easy|medium|hard" }

// Response
{ "explanation": "...", "topic": "Go channels", "level": "medium", "mastery": 0.52 }
```

### `POST /api/evaluate`
```json
// Request
{ "topic": "closures", "question": "What is a closure?", "answer": "A function that..." }

// Response
{ "correct": true, "score": 0.9, "feedback": "...", "key_points": ["..."],
  "new_mastery": 0.61, "difficulty": "medium" }
```

### `POST /api/quiz/start`
```json
// Request
{ "topic": "recursion" }

// Response
{ "session_id": "a3f8b21c", "question": "...", "difficulty": "easy", "mastery": 0.3 }
```

### `POST /api/quiz/answer`
```json
// Request
{ "session_id": "a3f8b21c", "answer": "base case stops recursion when..." }

// Response
{ "correct": true, "feedback": "...", "score": 0.85,
  "new_mastery": 0.41, "difficulty": "medium",
  "done": false, "next_question": "..." }
```

### `GET /api/progress`
```json
{ "topics": [
    { "topic": "closures", "mastery": 0.72, "attempts": 14, "difficulty": "medium" }
  ]
}
```

### `GET /api/weak-areas`
```json
{ "weak_areas": [
    { "topic": "pointers", "mastery": 0.21, "difficulty": "easy" }
  ]
}
```

---

## Network Security

After running `setup.sh <MAIN_MACHINE_IP>`, UFW will:

```
ALLOW  from <MAIN_IP>  to any port 7331   # Diamond
ALLOW  from <MAIN_IP>  to any port 11434  # Ollama (direct, optional)
DENY   in port 7331                        # block all others
DENY   in port 11434                       # block all others
```

If you use Tailscale instead of LAN, the external machine's Tailscale IP is stable — use that as `MAIN_MACHINE_IP` and as the `server` in the Neovim plugin.

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|-------------|-----|
| `Diamond ✗ server unreachable` | Server not running | `systemctl start diamond` or check IP/port |
| `ollama: unreachable` | Ollama not running | `systemctl start ollama` |
| Float says `Thinking...` forever | Request timed out (120s) | Model may be loading; wait or use a smaller model |
| `session not found` | Session expired or server restarted | Start a new quiz with `<leader>dq` |
| Mastery stuck at 0.5 | Only one attempt recorded | Play at least 3–5 questions per topic |
| Port 7331 refused | UFW blocking | Run setup.sh with your main machine IP |

---

## Future Extensions

- **Spaced repetition** — schedule review sessions based on forgetting curve  
- **Test-case runner** — submit code, run against hidden test cases, evaluate output  
- **Obsidian plugin** — right-click a note heading → explain / quiz  
- **Voice mode** — `whisper.cpp` for speech input, `piper` TTS for audio output  
- **Multi-model routing** — send code questions to `qwen2.5-coder`, general questions to `llama3.2`  
- **Vault indexing** — embed vault notes and use them as RAG context for explanations  
