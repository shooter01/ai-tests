package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type PRFile struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

type PRInput struct {
	ID          int      `json:"id"`
	Repo        string   `json:"repo"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	BaseBranch  string   `json:"base_branch"`
	HeadBranch  string   `json:"head_branch"`
	Files       []PRFile `json:"files"`
}

type StartJobRequest struct {
	Skill string  `json:"skill"`
	PR    PRInput `json:"pr"`
}

type ReviewFinding struct {
	Severity   string `json:"severity"`
	Path       string `json:"path"`
	Title      string `json:"title"`
	Comment    string `json:"comment"`
	Confidence string `json:"confidence"`
}

type ReviewOutput struct {
	Summary     string          `json:"summary"`
	Findings    []ReviewFinding `json:"findings"`
	Suggestions []string        `json:"suggestions"`
}

type Job struct {
	ID        string        `json:"job_id"`
	Status    string        `json:"status"`
	Skill     string        `json:"skill"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Result    *ReviewOutput `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type JobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewJobStore() *JobStore {
	return &JobStore{jobs: make(map[string]*Job)}
}

func (s *JobStore) Create(skill string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("job-%d", time.Now().UnixNano())
	job := &Job{
		ID:        id,
		Status:    "queued",
		Skill:     skill,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	s.jobs[id] = job
	return job
}

func (s *JobStore) SetRunning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[id]; ok {
		job.Status = "running"
		job.UpdatedAt = time.Now().UTC()
	}
}

func (s *JobStore) SetDone(id string, result *ReviewOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[id]; ok {
		job.Status = "done"
		job.Result = result
		job.Error = ""
		job.UpdatedAt = time.Now().UTC()
	}
}

func (s *JobStore) SetError(id, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[id]; ok {
		job.Status = "error"
		job.Error = errText
		job.UpdatedAt = time.Now().UTC()
	}
}

func (s *JobStore) Get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *job
	return &cp, true
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated]"
}

func buildPrompt(pr PRInput) string {
	var b strings.Builder

	b.WriteString("You are a strict senior code reviewer.\n")
	b.WriteString("Review this pull request diff and return strict JSON only.\n")
	b.WriteString("Do not wrap the answer in markdown fences.\n")
	b.WriteString("Do not add commentary before or after the JSON.\n")
	b.WriteString("The response must start with { and end with }.\n\n")

	b.WriteString("Return exactly this structure:\n")
	b.WriteString("{\n")
	b.WriteString(`  "summary": "string",` + "\n")
	b.WriteString(`  "findings": [` + "\n")
	b.WriteString(`    {` + "\n")
	b.WriteString(`      "severity": "high|medium|low",` + "\n")
	b.WriteString(`      "path": "string",` + "\n")
	b.WriteString(`      "title": "string",` + "\n")
	b.WriteString(`      "comment": "string",` + "\n")
	b.WriteString(`      "confidence": "high|medium|low"` + "\n")
	b.WriteString(`    }` + "\n")
	b.WriteString(`  ],` + "\n")
	b.WriteString(`  "suggestions": ["string"]` + "\n")
	b.WriteString("}\n\n")

	b.WriteString("Rules:\n")
	b.WriteString("- Only analyze visible facts from the diff.\n")
	b.WriteString("- Do not invent hidden code, hidden files, hidden flows, or hidden bugs.\n")
	b.WriteString("- Prioritize semantic changes over style comments.\n")
	b.WriteString("- Pay special attention to:\n")
	b.WriteString("  - changed return values\n")
	b.WriteString("  - nil -> error changes\n")
	b.WriteString("  - changed function contracts\n")
	b.WriteString("  - changed HTTP response behavior\n")
	b.WriteString("  - compatibility risks for callers\n")
	b.WriteString("- If a function previously returned nil and now returns an error, treat it as a potentially important semantic change.\n")
	b.WriteString("- Do not ignore behavior changes even if input validation changes are also present.\n")
	b.WriteString("- Before deciding there are no findings, explicitly check:\n")
	b.WriteString("  - return value changes\n")
	b.WriteString("  - error behavior changes\n")
	b.WriteString("  - API contract changes\n")
	b.WriteString("  - caller compatibility risks\n")
	b.WriteString("- If there are no reliable findings, return an empty findings array.\n")
	b.WriteString("- Maximum 5 findings.\n")
	b.WriteString("- Maximum 3 suggestions.\n")
	b.WriteString("- Do not give generic advice like 'add tests' or 'improve readability' unless the diff clearly justifies it.\n\n")

	b.WriteString("Repository:\n")
	b.WriteString(pr.Repo + "\n\n")

	b.WriteString("Pull Request Title:\n")
	b.WriteString(pr.Title + "\n\n")

	b.WriteString("Pull Request Description:\n")
	b.WriteString(pr.Description + "\n\n")

	b.WriteString("Base branch:\n")
	b.WriteString(pr.BaseBranch + "\n\n")

	b.WriteString("Head branch:\n")
	b.WriteString(pr.HeadBranch + "\n\n")

	b.WriteString("Changed files and diffs:\n")
	for _, f := range pr.Files {
		b.WriteString("\nFile: " + f.Path + "\n")
		b.WriteString("```diff\n")
		b.WriteString(truncate(f.Patch, 4000))
		b.WriteString("\n```\n")
	}

	return b.String()
}

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

type OpenCodeEvent struct {
	Type string `json:"type"`
	Part struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"part"`
}

func runOpenCodeAndCollectText(
	ctx context.Context,
	opencodeBin string,
	model string,
	prompt string,
	projectDir string,
) (string, string, error) {
	args := []string{
		"run",
		"--model", model,
		"--format", "json",
		prompt,
	}

	cmd := exec.CommandContext(ctx, opencodeBin, args...)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "OPENCODE_DISABLE_DEFAULT_PLUGINS=true")

	out, err := cmd.CombinedOutput()
	raw := string(out)
	if err != nil {
		return "", raw, fmt.Errorf("opencode failed: %w", err)
	}

	var finalText strings.Builder

	scanner := bufio.NewScanner(strings.NewReader(raw))
	// На всякий случай увеличим буфер, если ответ будет длиннее дефолтных 64KB.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var ev OpenCodeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// Пропускаем не-JSON строки, если вдруг они попадутся.
			continue
		}

		if ev.Type == "text" && ev.Part.Type == "text" {
			finalText.WriteString(ev.Part.Text)
		}
	}

	if err := scanner.Err(); err != nil {
		return "", raw, fmt.Errorf("scan opencode output failed: %w", err)
	}

	text := strings.TrimSpace(finalText.String())
	if text == "" {
		return "", raw, fmt.Errorf("no text response found in opencode events")
	}

	return text, raw, nil
}

func runOpenCode(jobID string, req StartJobRequest, store *JobStore, opencodeBin, model, agent, projectDir string) {
	store.SetRunning(jobID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	prompt := buildPrompt(req.PR)

	// Если позже захочешь реально использовать agent, можно добавить:
	// prompt = "Use the review workflow.\n\n" + prompt
	_ = agent // пока не используем, чтобы не было tool/skill-шума

	text, raw, err := runOpenCodeAndCollectText(ctx, opencodeBin, model, prompt, projectDir)
	if err != nil {
		store.SetError(jobID, err.Error()+"; raw: "+raw)
		return
	}

	var result ReviewOutput
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		store.SetError(jobID, "parse review json failed: "+err.Error()+"; text: "+text+"; raw: "+raw)
		return
	}

	store.SetDone(jobID, &result)
}

func main() {
	port := getenv("PORT", "8081")
	opencodeBin := getenv("OPENCODE_BIN", "opencode")
	model := getenv("OPENCODE_MODEL", "ollama/qwen2.5-coder:7b")
	agent := getenv("OPENCODE_AGENT", "review")
	projectDir := getenv("PROJECT_DIR", ".")

	store := NewJobStore()
	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"service":        "factory",
			"status":         "ok",
			"opencode_bin":   opencodeBin,
			"opencode_model": model,
			"opencode_agent": agent,
			"project_dir":    projectDir,
		})
	})

	r.POST("/jobs", func(c *gin.Context) {
		var req StartJobRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "bad json: " + err.Error()})
			return
		}

		req.Skill = strings.TrimSpace(req.Skill)
		if req.Skill == "" {
			c.JSON(400, gin.H{"error": "skill is required"})
			return
		}
		if req.PR.ID <= 0 {
			c.JSON(400, gin.H{"error": "pr.id is required"})
			return
		}

		job := store.Create(req.Skill)
		go runOpenCode(job.ID, req, store, opencodeBin, model, agent, projectDir)

		c.JSON(202, gin.H{
			"job_id": job.ID,
			"status": job.Status,
		})
	})

	r.GET("/jobs/:id", func(c *gin.Context) {
		job, ok := store.Get(c.Param("id"))
		if !ok {
			c.JSON(404, gin.H{"error": "job not found"})
			return
		}
		c.JSON(200, job)
	})

	_ = r.Run(":" + port)
}
