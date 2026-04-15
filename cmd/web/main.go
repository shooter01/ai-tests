package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
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

type StartJobResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func samplePR(id int) PRInput {
	return PRInput{
		ID:          id,
		Repo:        "example/repo",
		Title:       "Fix nil handling in enterprise member removal",
		Description: "Добавил проверки параметров и изменил поведение при отсутствии участника enterprise.",
		BaseBranch:  "main",
		HeadBranch:  "feature/enterprise-delete-fix",
		Files: []PRFile{
			{
				Path: "services/enterprise/delete.go",
				Patch: `@@ -10,6 +10,14 @@
 func DeleteEnterpriseMember(ctx *context.Context, enterpriseID, userID int64) error {
+   if enterpriseID == 0 {
+       return fmt.Errorf("enterprise id is required")
+   }
+
    member, err := getEnterpriseMember(ctx, enterpriseID, userID)
    if err != nil {
        return err
    }
@@ -40,7 +48,7 @@
-   if member == nil {
-       return nil
+   if member == nil {
+       return ErrMemberNotFound
    }

    return removeFromAllOrgs(ctx, member)
}
`,
			},
			{
				Path: "routers/api/enterprise.go",
				Patch: `@@ -22,6 +22,10 @@
 func DeleteEnterpriseMember(ctx *context.Context) {
    enterpriseID := ctx.PathParamInt64("enterpriseID")
    userID := ctx.PathParamInt64("userID")
+
+   if enterpriseID <= 0 || userID <= 0 {
+       ctx.JSON(http.StatusBadRequest, map[string]string{"message": "bad params"})
+       return
+   }

    err := enterprise_service.DeleteEnterpriseMember(ctx, enterpriseID, userID)
    if err != nil {
        handleError(ctx, err)
`,
			},
		},
	}
}

func main() {
	port := getenv("PORT", "8080")
	factoryURL := getenv("FACTORY_URL", "http://localhost:8081")
	client := &http.Client{Timeout: 2 * time.Minute}

	r := gin.Default()
	r.LoadHTMLGlob("templates/*")
	r.Static("/static", "./static")

	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/pr/1")
	})

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "web",
			"status":  "ok",
			"factory": factoryURL,
		})
	})

	r.GET("/pr/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			c.String(http.StatusBadRequest, "bad pr id")
			return
		}

		c.HTML(http.StatusOK, "pr.tmpl", gin.H{
			"PR": samplePR(id),
		})
	})

	r.POST("/pr/:id/review", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad pr id"})
			return
		}

		reqBody := StartJobRequest{
			Skill: "review",
			PR:    samplePR(id),
		}

		payload, err := json.Marshal(reqBody)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "marshal request failed"})
			return
		}

		req, err := http.NewRequestWithContext(
			c.Request.Context(),
			http.MethodPost,
			factoryURL+"/jobs",
			bytes.NewReader(payload),
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "create request failed"})
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "factory unavailable: " + err.Error()})
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read factory response"})
			return
		}

		c.Data(resp.StatusCode, "application/json", body)
	})

	r.GET("/jobs/:id", func(c *gin.Context) {
		req, err := http.NewRequestWithContext(
			c.Request.Context(),
			http.MethodGet,
			factoryURL+"/jobs/"+c.Param("id"),
			nil,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "create request failed"})
			return
		}

		resp, err := client.Do(req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "factory unavailable: " + err.Error()})
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read factory response"})
			return
		}

		c.Data(resp.StatusCode, "application/json", body)
	})

	_ = r.Run(":" + port)
}
