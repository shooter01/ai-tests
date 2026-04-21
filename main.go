package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
)

var reviewPrompt string

type reviewIssue struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

type reviewResult struct {
	Summary string        `json:"summary"`
	Issues  []reviewIssue `json:"issues"`
	Verdict string        `json:"verdict"`
}

var trailingCommaRe = regexp.MustCompile(`,(\s*[\]}])`)

func sanitizeJSON(s string) string {
	s = strings.TrimSpace(s)

	// снимаем ```json / ``` в начале и ``` в конце
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// отрезаем текст до первого '{' и после последнего '}'
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 {
		s = s[:j+1]
	}

	// чиним висячие запятые: "...},]" и "...],}"
	s = trailingCommaRe.ReplaceAllString(s, "$1")

	return s
}
func formatComment(reviewJSON string) string {
	var r reviewResult
	var r reviewResult
	if err := json.Unmarshal([]byte(reviewJSON), &r); err != nil {
		log.Printf("formatComment: JSON unmarshal failed: %v", err)
		return "### 🤖 Auto review (GigaChat)\n\n" +
			"_Модель вернула невалидный JSON, ниже сырой ответ:_\n\n" +
			"```\n" + reviewJSON + "\n```"
	}

	var b strings.Builder

	// шапка
	b.WriteString("### 🤖 Auto review (GigaChat)\n\n")

	// вердикт эмодзи
	switch strings.ToLower(r.Verdict) {
	case "approve":
		b.WriteString("**Verdict:** ✅ approve\n\n")
	case "request_changes":
		b.WriteString("**Verdict:** ❌ request changes\n\n")
	default:
		b.WriteString("**Verdict:** " + r.Verdict + "\n\n")
	}

	// summary
	if r.Summary != "" {
		b.WriteString("**Summary:** " + r.Summary + "\n\n")
	}

	// если issues нет — короткий ответ и уходим
	if len(r.Issues) == 0 {
		b.WriteString("_No issues found._\n")
		return b.String()
	}

	// таблица issues
	b.WriteString("| # | Severity | Category | File:Line | Problem |\n")
	b.WriteString("|---|----------|----------|-----------|---------|\n")
	for i, iss := range r.Issues {
		loc := iss.File
		if iss.Line > 0 {
			loc = fmt.Sprintf("%s:%d", iss.File, iss.Line)
		}
		b.WriteString(fmt.Sprintf(
			"| %d | %s | `%s` | `%s` | %s |\n",
			i+1, sevEmoji(iss.Severity), iss.Category, loc, escapeCell(iss.Message),
		))
	}
	b.WriteString("\n")

	// подробности с suggestion
	b.WriteString("<details><summary>Details & suggestions</summary>\n\n")
	for i, iss := range r.Issues {
		loc := iss.File
		if iss.Line > 0 {
			loc = fmt.Sprintf("%s:%d", iss.File, iss.Line)
		}
		fmt.Fprintf(&b, "**%d. %s — `%s` at `%s`**\n\n", i+1, sevEmoji(iss.Severity), iss.Category, loc)
		fmt.Fprintf(&b, "- Problem: %s\n", iss.Message)
		if iss.Suggestion != "" {
			fmt.Fprintf(&b, "- Suggestion: %s\n", iss.Suggestion)
		}
		b.WriteString("\n")
	}
	b.WriteString("</details>\n")

	return b.String()
}

func sevEmoji(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "🔴 critical"
	case "high":
		return "🟠 high"
	case "medium":
		return "🟡 medium"
	case "low":
		return "🟢 low"
	}
	return s
}

// экранируем символы, которые ломают markdown-таблицы
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	// срезаем стартовые ```json / ```
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func loadReviewPrompt() {
	path := getenv("REVIEW_PROMPT_PATH", "./agents/review.md")
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read review prompt %s: %v", path, err)
	}
	reviewPrompt = strings.TrimSpace(string(data))
	log.Printf("review prompt loaded: %d bytes from %s", len(reviewPrompt), path)
}

type gigaChatRequest struct {
	Model       string            `json:"model"`
	Messages    []gigaChatMessage `json:"messages"`
	Temperature *float64          `json:"temperature,omitempty"`
}

type gigaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type gigaChatResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// ---------- конфиг через env ----------

var (
	addr          string
	webhookSecret string
	giteaBaseURL  string
	giteaToken    string
	// gigachatToken string
	opencodeImage string
	opencodeModel string
	opencodeAgent string
	workdirOnHost string
)

var (
	gigachatAuthKey string
	gigachatScope   string

	gigaMu     sync.Mutex
	gigaToken  string
	gigaExpiry time.Time
)

func loadConfig() {
	if err := godotenv.Load(); err != nil {
		log.Printf(".env not loaded: %v (using process env only)", err)
	}
	addr = getenv("ADDR", ":8080")
	webhookSecret = os.Getenv("GITEA_WEBHOOK_SECRET")
	giteaBaseURL = getenv("GITEA_BASE_URL", "http://localhost:3000")
	giteaToken = os.Getenv("GITEA_TOKEN")
	// gigachatToken = os.Getenv("GIGACHAT_TOKEN")
	opencodeImage = getenv("OPENCODE_IMAGE", "opencode-giga")
	opencodeModel = getenv("OPENCODE_MODEL", "gigachat/GigaChat-2")
	opencodeAgent = getenv("OPENCODE_AGENT", "review")
	workdirOnHost = getenv("WORKDIR_ON_HOST", mustCwd())
	gigachatAuthKey = os.Getenv("GIGACHAT_AUTH_KEY")
	gigachatScope = getenv("GIGACHAT_SCOPE", "GIGACHAT_API_PERS")
	if gigachatAuthKey == "" {
		log.Fatal("GIGACHAT_AUTH_KEY is required (base64 of client_id:client_secret)")
	}
}

func getGigachatToken(ctx context.Context) (string, error) {
	gigaMu.Lock()
	defer gigaMu.Unlock()

	// запас 2 минуты, чтобы не попасть в гонку
	if gigaToken != "" && time.Now().Before(gigaExpiry.Add(-2*time.Minute)) {
		return gigaToken, nil
	}

	form := strings.NewReader("scope=" + gigachatScope)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://ngw.devices.sberbank.ru:9443/api/v2/oauth", form)
	req.Header.Set("Authorization", "Basic "+gigachatAuthKey)
	req.Header.Set("RqUID", uuid.NewString()) // github.com/google/uuid
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// небезопасный TLS — как у opencode-контейнера, чтобы без сертификатов Минцифры
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := &http.Client{Transport: tr, Timeout: 15 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("gigachat oauth %d: %s", resp.StatusCode, string(body))
	}

	var r struct {
		AccessToken string `json:"access_token"`
		ExpiresAt   int64  `json:"expires_at"` // unix millis
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("parse gigachat oauth: %w", err)
	}
	gigaToken = r.AccessToken
	gigaExpiry = time.UnixMilli(r.ExpiresAt)
	log.Printf("gigachat token refreshed, expires at %s", gigaExpiry.Format(time.RFC3339))
	return gigaToken, nil
}

const gvAcceptHeader = "application/vnd.gitverse.object+json;version=1"

func setGiteaHeaders(req *http.Request) {
	req.Header.Set("Accept", gvAcceptHeader)
	if giteaToken != "" {
		req.Header.Set("Authorization", "Bearer "+giteaToken)
	}
}

func fetchDiff(repoFullName string, prNumber int) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/pulls/%d/files", giteaBaseURL, repoFullName, prNumber)
	log.Printf("fetch files: GET %s", url)

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	setGiteaHeaders(req)
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("files %d for %s: %s", resp.StatusCode, url, strings.TrimSpace(string(raw)))
	}

	var files []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Patch     string `json:"patch"`
	}
	if err := json.Unmarshal(raw, &files); err != nil {
		return "", fmt.Errorf("parse files: %w", err)
	}

	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "### %s (%s, +%d/-%d)\n", f.Filename, f.Status, f.Additions, f.Deletions)
		if f.Patch == "" {
			b.WriteString("(no patch — binary or too large)\n\n")
			continue
		}
		b.WriteString(f.Patch)
		b.WriteString("\n\n")
	}
	return b.String(), nil
}

func postComment(repoFullName string, prNumber int, body string) error {
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", giteaBaseURL, repoFullName, prNumber)
	payload, _ := json.Marshal(map[string]string{"body": body})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	setGiteaHeaders(req)

	resp, err := httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("comment %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// ---------- payload Gitea (только нужные поля) ----------

type giteaPRHook struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		State string `json:"state"`
		User  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"user"`
	} `json:"pullRequest"`
	Repository struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		OwnerName string `json:"ownerName"`
		FullName  string `json:"fullName"`
	} `json:"repository"`
	Sender struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"sender"`
	CommitID string `json:"commitId"`
}

// ---------- main ----------

func main() {
	loadConfig()
	loadReviewPrompt()

	http.HandleFunc("/hook", handleHook)
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	// --- логируем входящий запрос ---
	event := r.Header.Get("X-Gitea-Event")
	delivery := r.Header.Get("X-Gitea-Delivery")
	sig := r.Header.Get("X-Gitea-Signature")
	log.Printf("hook in: event=%q delivery=%q sig_len=%d content_type=%q body_bytes=%d remote=%s",
		event, delivery, len(sig), r.Header.Get("Content-Type"), len(body), r.RemoteAddr)
	log.Printf("hook body: %s", prettyOrRaw(body, 4000))
	// --------------------------------

	if webhookSecret != "" && !verifySig(sig, body, webhookSecret) {
		log.Printf("hook rejected: bad signature (delivery=%s)", delivery)
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	if event == "" {
		// форк не шлёт X-Gitea-Event — определяем по payload
		if len(body) > 0 && bytes.Contains(body, []byte(`"pullRequest"`)) {
			event = "pull_request"
			log.Printf("hook: X-Gitea-Event is empty, inferred event=pull_request from payload")
		}
	}
	if event != "pull_request" {
		log.Printf("hook skipped: event=%q", event)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var p giteaPRHook
	if err := json.Unmarshal(body, &p); err != nil {
		log.Printf("hook parse error: %v", err)
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	if p.Action != "opened" && p.Action != "synchronize" && p.Action != "reopened" {
		log.Printf("hook skipped: action=%s", p.Action)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if p.Repository.FullName == "" || p.Number == 0 {
		log.Printf("hook parse warning: fullName=%q number=%d — unexpected payload", p.Repository.FullName, p.Number)
		http.Error(w, "unexpected payload shape", http.StatusBadRequest)
		return
	}

	log.Printf("hook accepted: pr=#%d repo=%s action=%s author=%s",
		p.Number, p.Repository.FullName, p.Action, p.PullRequest.User.Name)

	go processPR(p)
	w.WriteHeader(http.StatusAccepted)
}
func processPR(p giteaPRHook) {
	repo := p.Repository.FullName
	log.Printf("PR #%d in %s: %s", p.Number, repo, p.PullRequest.Title)

	diff, err := fetchDiff(repo, p.Number)
	if err != nil {
		log.Printf("fetch diff: %v", err)
		return
	}

	prompt := fmt.Sprintf(
		"%s\n\n"+
			"=== TASK ===\n"+
			"PR: %s\n"+
			"Проанализируй следующий diff согласно инструкциям выше. "+
			"Верни ТОЛЬКО JSON, как описано в формате ответа.\n\n"+
			"=== BEGIN DIFF ===\n%s\n=== END DIFF ===",
		reviewPrompt, p.PullRequest.Title, diff,
	)

	reviewJSON, err := runOpencode(prompt)
	if err != nil {
		log.Printf("opencode: %v", err)
		return
	}

	reviewJSON = sanitizeJSON(reviewJSON)

	if strings.TrimSpace(reviewJSON) == "" {
		log.Printf("opencode returned empty output, skip comment")
		return
	}

	// formatComment сам распарсит. Не парсит — отдаст fallback с текстом.
	if err := postComment(repo, p.Number, formatComment(reviewJSON)); err != nil {
		log.Printf("post comment: %v", err)
	}
	reviewJSON = stripCodeFence(reviewJSON)

	if err := postComment(repo, p.Number, formatComment(reviewJSON)); err != nil {
		log.Printf("post comment: %v", err)
	}
}

// ---------- Gitea API ----------

// ---------- запуск opencode ----------

func runOpencode(prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tok, err := getGigachatToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get gigachat token: %w", err)
	}

	args := []string{
		"run", "--rm", "-i",
		"-e", "GIGACHAT_TOKEN=" + tok,
		"-e", "NODE_TLS_REJECT_UNAUTHORIZED=0",
		"-v", workdirOnHost + ":/work",
		"-w", "/work",
		opencodeImage,
		"run", "--model", opencodeModel, prompt,
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	out := strings.TrimSpace(stdout.String())
	errOut := strings.TrimSpace(stderr.String())

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return out, fmt.Errorf("opencode timeout; stderr: %s", errOut)
		}
		return out, fmt.Errorf("docker run failed: %v; stderr: %s", err, errOut)
	}

	if errOut != "" {
		log.Printf("opencode stderr: %s", truncate(errOut, 1024))
	}

	return out, nil
}

// truncate — маленькая утилита для обрезки длинного stderr/stdout в логах.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}

// ---------- утилиты ----------

func verifySig(got string, body []byte, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(got), []byte(expected))
}

func httpClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustCwd() string {
	d, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	return d
}

// prettyOrRaw пытается красиво отформатировать JSON; если не JSON — режет до limit байт.
func prettyOrRaw(b []byte, limit int) string {
	var pretty bytes.Buffer
	if json.Valid(b) {
		_ = json.Indent(&pretty, b, "", "  ")
		s := pretty.String()
		if len(s) > limit {
			return s[:limit] + "\n...[truncated]"
		}
		return s
	}
	s := string(b)
	if len(s) > limit {
		return s[:limit] + "...[truncated]"
	}
	return s
}
