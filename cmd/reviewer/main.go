package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

type OllamaRequest struct {
	Model     string `json:"model"`
	System    string `json:"system,omitempty"`
	Prompt    string `json:"prompt"`
	Stream    bool   `json:"stream"`
	Format    any    `json:"format,omitempty"`
	KeepAlive string `json:"keep_alive,omitempty"`
}

type OllamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func getenvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated]"
}

func normalizeSeverity(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	default:
		return "low"
	}
}

func normalizeConfidence(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	default:
		return "low"
	}
}

func isGenericSuggestion(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	bad := []string{
		"убедитесь",
		"улучшите читаемость",
		"добавьте тесты",
		"проверьте обработку ошибок",
	}
	for _, x := range bad {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func validateReview(out *ReviewOutput, allowedPaths map[string]struct{}) {
	out.Summary = strings.TrimSpace(out.Summary)

	findings := make([]ReviewFinding, 0, len(out.Findings))
	for _, f := range out.Findings {
		f.Path = strings.TrimSpace(f.Path)
		f.Title = strings.TrimSpace(f.Title)
		f.Comment = strings.TrimSpace(f.Comment)
		f.Severity = normalizeSeverity(f.Severity)
		f.Confidence = normalizeConfidence(f.Confidence)

		if f.Path == "" || f.Title == "" || f.Comment == "" {
			continue
		}
		if _, ok := allowedPaths[f.Path]; !ok {
			continue
		}
		if f.Confidence == "low" {
			continue
		}

		findings = append(findings, f)
		if len(findings) == 5 {
			break
		}
	}
	out.Findings = findings

	suggestions := make([]string, 0, len(out.Suggestions))
	for _, s := range out.Suggestions {
		s = strings.TrimSpace(s)
		if s == "" || isGenericSuggestion(s) {
			continue
		}
		suggestions = append(suggestions, s)
		if len(suggestions) == 3 {
			break
		}
	}

	if len(out.Findings) == 0 {
		suggestions = nil
	}
	out.Suggestions = suggestions
}

func buildReviewPrompt(pr PRInput, maxFiles, maxPatchChars int) string {
	var b strings.Builder

	b.WriteString("Проанализируй pull request и верни ТОЛЬКО JSON без markdown, без пояснений, без обрамления в ```.\n")
	b.WriteString("Ставь выше всего замечания, которые меняют поведение, контракт функции, возвращаемые ошибки, HTTP-статусы и логику ветвления.\n")
	b.WriteString("Не выдумывай скрытые баги, отсутствующие функции и не показанные в diff сценарии.\n")
	b.WriteString("Не отдавай приоритет формальным валидациям, если в diff есть более существенное изменение поведения.\n")
	b.WriteString("Если надежных замечаний нет, верни пустой findings.\n\n")

	b.WriteString("Строгая JSON-структура ответа:\n")
	b.WriteString("{\n")
	b.WriteString(`  "summary": "string",` + "\n")
	b.WriteString(`  "findings": [` + "\n")
	b.WriteString("    {\n")
	b.WriteString(`      "severity": "high|medium|low",` + "\n")
	b.WriteString(`      "path": "string",` + "\n")
	b.WriteString(`      "title": "string",` + "\n")
	b.WriteString(`      "comment": "string",` + "\n")
	b.WriteString(`      "confidence": "high|medium|low"` + "\n")
	b.WriteString("    }\n")
	b.WriteString("  ],\n")
	b.WriteString(`  "suggestions": ["string"]` + "\n")
	b.WriteString("}\n\n")

	b.WriteString("Правила:\n")
	b.WriteString("- summary: 1-3 предложения.\n")
	b.WriteString("- findings: максимум 5.\n")
	b.WriteString("- path должен быть одним из файлов из diff.\n")
	b.WriteString("- suggestions: максимум 3 коротких совета.\n\n")

	b.WriteString("Контекст PR:\n")
	b.WriteString(fmt.Sprintf("Repository: %s\n", pr.Repo))
	b.WriteString(fmt.Sprintf("Title: %s\n", pr.Title))
	b.WriteString(fmt.Sprintf("Description: %s\n", pr.Description))
	b.WriteString(fmt.Sprintf("Base branch: %s\n", pr.BaseBranch))
	b.WriteString(fmt.Sprintf("Head branch: %s\n\n", pr.HeadBranch))

	files := pr.Files
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}

	b.WriteString("Измененные файлы и diff:\n")
	for i, f := range files {
		b.WriteString(fmt.Sprintf("\n### File %d: %s\n", i+1, f.Path))
		b.WriteString("```diff\n")
		b.WriteString(truncate(f.Patch, maxPatchChars))
		b.WriteString("\n```\n")
	}

	if len(pr.Files) > maxFiles {
		b.WriteString(fmt.Sprintf("\nПоказаны только первые %d файлов из %d.\n", maxFiles, len(pr.Files)))
	}

	return b.String()
}

func runReview(inputPath, outputPath string) error {
	ollamaURL := getenv("OLLAMA_URL", "http://ollama:11434/api/generate")
	model := getenv("MODEL", "qwen2.5-coder:7b")
	maxFiles := getenvInt("MAX_FILES", 5)
	maxPatchChars := getenvInt("MAX_PATCH_CHARS", 4000)

	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read input file: %w", err)
	}

	var pr PRInput
	if err := json.Unmarshal(raw, &pr); err != nil {
		return fmt.Errorf("parse input json: %w", err)
	}

	allowedPaths := make(map[string]struct{}, len(pr.Files))
	for _, f := range pr.Files {
		allowedPaths[f.Path] = struct{}{}
	}

	systemPrompt := `Ты строгий senior reviewer.
Анализируй только факты из diff.
Главный приоритет — изменения поведения:
- возврат новых ошибок,
- изменение контрактов,
- изменение HTTP-статусов,
- изменение ветвления,
- изменение семантики nil/not found,
- потенциальная несовместимость с существующим кодом.

Не выдумывай несуществующие сценарии.
Не давай общие советы без привязки к изменениям.
Если уверенность низкая — не добавляй finding.
Ответ должен быть строго валидным JSON и больше ничем.`

	reqBody := OllamaRequest{
		Model:     model,
		System:    systemPrompt,
		Prompt:    buildReviewPrompt(pr, maxFiles, maxPatchChars),
		Stream:    false,
		Format:    "json",
		KeepAlive: "5m",
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest(http.MethodPost, ollamaURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call ollama: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read ollama response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama status %d: %s", resp.StatusCode, string(body))
	}

	var rawOut OllamaResponse
	if err := json.Unmarshal(body, &rawOut); err != nil {
		return fmt.Errorf("parse ollama envelope: %w; raw: %s", err, string(body))
	}

	var out ReviewOutput
	if err := json.Unmarshal([]byte(rawOut.Response), &out); err != nil {
		return fmt.Errorf("parse model json: %w; raw: %s", err, rawOut.Response)
	}

	validateReview(&out, allowedPaths)

	finalJSON, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal final json: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("mkdir output dir: %w", err)
	}

	if err := os.WriteFile(outputPath, finalJSON, 0o644); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}

	return nil
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: reviewer <skill> <inputPath> <outputPath>")
		os.Exit(2)
	}

	skill := strings.TrimSpace(os.Args[1])
	inputPath := os.Args[2]
	outputPath := os.Args[3]

	switch skill {
	case "review":
		if err := runReview(inputPath, outputPath); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "unsupported skill:", skill)
		os.Exit(2)
	}
}
