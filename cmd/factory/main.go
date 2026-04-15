package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	return &JobStore{
		jobs: make(map[string]*Job),
	}
}

func (s *JobStore) Create(skill string) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	id := fmt.Sprintf("job-%d", now.UnixNano())
	job := &Job{
		ID:        id,
		Status:    "queued",
		Skill:     skill,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.jobs[id] = job
	return job
}

func (s *JobStore) SetRunning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return
	}
	job.Status = "running"
	job.UpdatedAt = time.Now().UTC()
}

func (s *JobStore) SetDone(id string, result *ReviewOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return
	}
	job.Status = "done"
	job.Result = result
	job.Error = ""
	job.UpdatedAt = time.Now().UTC()
}

func (s *JobStore) SetError(id, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return
	}
	job.Status = "error"
	job.Error = errText
	job.UpdatedAt = time.Now().UTC()
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

func dockerPath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

func runReviewJob(jobID string, req StartJobRequest, store *JobStore, reviewerImage, dockerNetwork, model, tmpRoot string) {
	store.SetRunning(jobID)

	jobDir := filepath.Join(tmpRoot, jobID)
	inputDir := filepath.Join(jobDir, "input")
	outputDir := filepath.Join(jobDir, "output")

	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		store.SetError(jobID, "mkdir input dir: "+err.Error())
		return
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		store.SetError(jobID, "mkdir output dir: "+err.Error())
		return
	}

	inputPath := filepath.Join(inputDir, "pr.json")
	outputPath := filepath.Join(outputDir, "review.json")

	rawInput, err := json.MarshalIndent(req.PR, "", "  ")
	if err != nil {
		store.SetError(jobID, "marshal pr json: "+err.Error())
		return
	}
	if err := os.WriteFile(inputPath, rawInput, 0o644); err != nil {
		store.SetError(jobID, "write input file: "+err.Error())
		return
	}

	absInputDir, err := filepath.Abs(inputDir)
	if err != nil {
		store.SetError(jobID, "abs input dir: "+err.Error())
		return
	}
	absOutputDir, err := filepath.Abs(outputDir)
	if err != nil {
		store.SetError(jobID, "abs output dir: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	args := []string{
		"run",
		"--rm",
		"--network", dockerNetwork,
		"--mount", "type=bind,source=" + dockerPath(absInputDir) + ",target=/input,readonly",
		"--mount", "type=bind,source=" + dockerPath(absOutputDir) + ",target=/output",
		"-e", "OLLAMA_URL=http://ollama:11434/api/generate",
		"-e", "MODEL=" + model,
		reviewerImage,
		req.Skill,
		"/input/pr.json",
		"/output/review.json",
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		store.SetError(jobID, "docker run failed: "+err.Error()+"; output: "+string(out))
		return
	}

	rawResult, err := os.ReadFile(outputPath)
	if err != nil {
		store.SetError(jobID, "read output file: "+err.Error())
		return
	}

	var result ReviewOutput
	if err := json.Unmarshal(rawResult, &result); err != nil {
		store.SetError(jobID, "parse output json: "+err.Error()+"; raw: "+string(rawResult))
		return
	}

	store.SetDone(jobID, &result)
}

func main() {
	port := getenv("PORT", "8081")
	reviewerImage := getenv("REVIEWER_IMAGE", "test-orch-reviewer:latest")
	dockerNetwork := getenv("DOCKER_NETWORK", "test-orch_default")
	model := getenv("MODEL", "qwen2.5-coder:7b")
	tmpRoot := getenv("TMP_ROOT", "./tmp/jobs")

	store := NewJobStore()

	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"service":        "factory",
			"status":         "ok",
			"reviewer_image": reviewerImage,
			"docker_network": dockerNetwork,
			"model":          model,
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

		go runReviewJob(job.ID, req, store, reviewerImage, dockerNetwork, model, tmpRoot)

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
