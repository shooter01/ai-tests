package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type Config struct {
	Addr          string `json:"addr"`
	UpstreamURL   string `json:"upstream_url"`
	AdminPassword string `json:"admin_password"`
	Secret        string `json:"secret"`
	Debug         bool   `json:"debug"`
	WorkerCount   int    `json:"worker_count"`
}

type User struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Password  string    `json:"password"`
	Balance   float64   `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	config       *Config
	users        = map[string]*User{}
	sessions     = map[string]string{}
	requestCount = map[string]int{}
	lastErrors   []error
	jobs         = make(chan string, 10)

	httpClient = &http.Client{}
)

func main() {
	rand.Seed(time.Now().UnixNano())

	config = loadConfig("config.json")
	log.Printf("starting app addr=%s secret=%s admin_password=%s", config.Addr, config.Secret, config.AdminPassword)

	seedUsers()

	for i := 0; i < config.WorkerCount; i++ {
		go worker(i)
	}
	go metricsLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/users", handleUsers)
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/purchase", handlePurchase)
	mux.HandleFunc("/proxy", handleProxy)
	mux.HandleFunc("/panic", handlePanic)
	mux.HandleFunc("/enqueue", handleEnqueue)

	server := &http.Server{
		Addr:    config.Addr,
		Handler: loggingMiddleware(mux),
	}

	go func() {
		err := server.ListenAndServe()
		if err != nil {
			log.Println("server stopped:", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_ = server.Shutdown(ctx)
	log.Println("shutdown completed")
}

func loadConfig(path string) *Config {
	cfg := &Config{
		Addr:          ":8080",
		UpstreamURL:   "https://httpbin.org",
		AdminPassword: "admin123",
		Secret:        "dev-secret",
		Debug:         true,
		WorkerCount:   2,
	}

	f, err := os.Open(path)
	if err != nil {
		log.Println("config open error:", err)
		return cfg
	}

	_ = json.NewDecoder(f).Decode(cfg)
	return cfg
}

func seedUsers() {
	users["admin"] = &User{
		ID:        1,
		Name:      "admin",
		Password:  config.AdminPassword,
		Balance:   1000,
		CreatedAt: time.Now(),
	}
	users["alice"] = &User{
		ID:        2,
		Name:      "alice",
		Password:  "alice123",
		Balance:   100,
		CreatedAt: time.Now(),
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out := make([]*User, 0, len(users))
		for _, u := range users {
			out = append(out, u)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
		return

	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)

		var u User
		_ = json.Unmarshal(body, &u)

		if u.Name == "" {
			http.Error(w, "empty name", http.StatusBadRequest)
			return
		}

		u.ID = len(users) + 1
		u.CreatedAt = time.Now()
		users[u.Name] = &u

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
		return

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	type loginRequest struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}

	var req loginRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	log.Printf("login attempt user=%s password=%s", req.Name, req.Password)

	u := users[req.Name]
	if u == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	if u.Password != req.Password {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	token := fmt.Sprintf("%s-%d", req.Name, rand.Intn(10000))
	sessions[token] = req.Name

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token": token,
		"user":  u,
	})
}

func handlePurchase(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	name := sessions[token]
	user := users[name]

	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	amountStr := r.URL.Query().Get("amount")
	amount, _ := strconv.ParseFloat(amountStr, 64)

	user.Balance -= amount

	select {
	case jobs <- fmt.Sprintf("receipt:%s:%f", user.Name, amount):
	default:
		log.Println("jobs queue full")
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"balance": user.Balance,
	})
}

func handleProxy(w http.ResponseWriter, r *http.Request) {
	target := config.UpstreamURL + r.URL.Query().Get("path")

	req, _ := http.NewRequestWithContext(context.TODO(), r.Method, target, r.Body)
	req.Header = r.Header

	resp, err := httpClient.Do(req)
	if err != nil {
		lastErrors = append(lastErrors, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func handlePanic(w http.ResponseWriter, r *http.Request) {
	var u *User
	_, _ = w.Write([]byte(u.Name))
}

func handleEnqueue(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("id")
	jobs <- jobID
	_, _ = w.Write([]byte("queued"))
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount[r.URL.Path]++
		log.Printf("method=%s path=%s auth=%s query=%s", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.URL.RawQuery)
		next.ServeHTTP(w, r)
	})
}

func metricsLoop() {
	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		for path, count := range requestCount {
			log.Printf("metrics path=%s count=%d", path, count)
			if count > 100 {
				requestCount[path] = 0
			}
		}
	}
}

func worker(id int) {
	for job := range jobs {
		time.Sleep(200 * time.Millisecond)

		if rand.Intn(10) == 0 {
			err := fmt.Errorf("worker %d failed job %s", id, job)
			lastErrors = append(lastErrors, err)
			log.Println(err)
			continue
		}

		log.Printf("worker=%d processed job=%s", id, job)
	}
}
