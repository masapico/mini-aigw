package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	v1 "mini-aigw/internal/api/v1"
	webapi "mini-aigw/internal/api/web"
	"mini-aigw/internal/config"
	"mini-aigw/internal/db"
	"mini-aigw/internal/services"
	"mini-aigw/internal/version"
	"mini-aigw/internal/views"
	"mini-aigw/web"
)

func main() {
	versionFlag := flag.Bool("version", false, "Print version information and exit")
	vFlag := flag.Bool("v", false, "Print version information and exit (shorthand)")
	flag.Parse()
	if *versionFlag || *vFlag {
		fmt.Printf("mini-aigw %s (commit: %s, built: %s)\n", version.Version, version.GitCommit, version.BuildTime)
		return
	}

	cfg := config.LoadConfig()

	log.Printf("Starting %s %s in environment (Debug=%v)...", cfg.ProjectName, version.Version, cfg.Debug)

	// 1. Initialize SQLite with WAL mode
	database, err := db.InitDB(cfg)
	if err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}

	// 2. Seed initial data
	if err := db.SeedInitialData(database, cfg); err != nil {
		log.Fatalf("Database seeding failed: %v", err)
	}

	// 3. Purge expired sessions
	middleware.PurgeExpiredSessions(database)

	// 4. Initialize template renderer
	renderer, err := views.InitRenderer()
	if err != nil {
		log.Fatalf("Renderer initialization failed: %v", err)
	}

	// 5. Initialize services
	ctx := context.Background()
	xberg := services.NewXbergService(cfg)

	awsBundle, err := services.NewAWSClientBundle(ctx, cfg)
	if err != nil {
		log.Fatalf("AWS client initialization failed: %v", err)
	}

	bedrockSvc := services.NewBedrockService(awsBundle, xberg, cfg)
	knowledgeSvc := services.NewKnowledgeService(awsBundle, database, cfg)

	oidcSvc, err := services.NewOIDCService(ctx, cfg)
	if err != nil {
		log.Printf("Warning: OIDC initialization error (OIDC login will be disabled): %v", err)
	}

	// 6. Build HTTP Router
	r := SetupRouter(cfg, database, renderer, bedrockSvc, knowledgeSvc, oidcSvc)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("%s server listening on http://%s", cfg.ProjectName, addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server ListenAndServe error: %v", err)
		}
	}()

	<-stopChan
	log.Println("Shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	log.Println("Server stopped")
}

func SetupRouter(
	cfg *config.Config,
	database *gorm.DB,
	renderer *views.Renderer,
	bedrockSvc *services.BedrockService,
	knowledgeSvc *services.KnowledgeService,
	oidcSvc *services.OIDCService,
) chi.Router {
	chatHandler := v1.NewChatHandler(bedrockSvc, knowledgeSvc, cfg, database)
	modelsHandler := v1.NewModelsHandler(database, cfg)
	downloadHandler := v1.NewKnowledgeDownloadHandler(knowledgeSvc, database, cfg)

	authHandler := webapi.NewAuthHandler(database, cfg, oidcSvc, renderer)
	dashboardHandler := webapi.NewDashboardHandler(database, cfg, renderer)
	knowledgeHandler := webapi.NewKnowledgeHandler(knowledgeSvc, database, cfg, renderer)
	ragTestHandler := webapi.NewRAGTestHandler(bedrockSvc, knowledgeSvc, database, cfg, renderer)
	apiKeysHandler := webapi.NewAPIKeysHandler(database, cfg, renderer)
	webModelsHandler := webapi.NewWebModelsHandler(database, renderer)
	usersHandler := webapi.NewUsersHandler(database, renderer)

	r := chi.NewRouter()

	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	r.Use(middleware.SessionAuthMiddleware(database, cfg))

	// Health check (ALB / EC2 AutoRecovery)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := fmt.Sprintf(`{"status":"healthy","database":"ok","version":"%s"}`, version.Version)
		_, _ = w.Write([]byte(resp))
	})

	// Static files & Favicon via embed.FS
	staticFS := http.FS(web.StaticFS())
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(staticFS)))
	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		data, err := web.Files.ReadFile("static/favicon.svg")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(data)
	})

	// OpenAI-compatible v1 API
	r.Route("/v1", func(v1Router chi.Router) {
		v1Router.Get("/models", modelsHandler.ListModels)
		v1Router.Get("/knowledge/files/{doc_id}/download", downloadHandler.DownloadFile)
		v1Router.Get("/knowledge/download/{doc_id}", downloadHandler.DownloadFile)

		// API Key Protected
		v1Router.Group(func(authRouter chi.Router) {
			authRouter.Use(middleware.VerifyAPIKey(database))
			authRouter.Post("/chat/completions", chatHandler.HandleChatCompletions)
		})
	})

	// Web UI Auth Routes
	r.Get("/login", authHandler.ShowLogin)
	r.Post("/login", authHandler.HandleLocalLogin)
	r.Get("/logout", authHandler.HandleLogout)
	r.Get("/auth/oidc/login", authHandler.HandleOIDCLogin)
	r.Get("/auth/oidc/callback", authHandler.HandleOIDCCallback)

	// Web UI Protected Routes (RequireAuth)
	r.Group(func(webRouter chi.Router) {
		webRouter.Use(middleware.RequireAuth)

		// Dashboard
		webRouter.Get("/", dashboardHandler.ShowDashboard)
		webRouter.Get("/dashboard", dashboardHandler.ShowDashboard)

		// Knowledge
		webRouter.Get("/knowledge", knowledgeHandler.ShowKnowledge)
		webRouter.Get("/knowledge/table", knowledgeHandler.RenderTable)
		webRouter.Post("/knowledge/upload", knowledgeHandler.HandleUpload)
		webRouter.Delete("/knowledge/{id}", knowledgeHandler.HandleDelete)
		webRouter.Get("/knowledge/sync/status", knowledgeHandler.GetSyncStatus)
		webRouter.Post("/knowledge/sync", knowledgeHandler.HandleSync)
		webRouter.Get("/knowledge/{id}/download", knowledgeHandler.HandleDownload)

		// RAG Test
		webRouter.Get("/rag-test", ragTestHandler.ShowRAGTest)
		webRouter.Post("/rag-test/retrieve", ragTestHandler.HandleRetrieve)
		webRouter.Post("/rag-test/chat", ragTestHandler.HandleChat)

		// Admin Only Routes
		webRouter.Group(func(adminRouter chi.Router) {
			adminRouter.Use(middleware.RequireAdmin)

			// API Keys
			adminRouter.Get("/api-keys", apiKeysHandler.ShowAPIKeys)
			adminRouter.Post("/api-keys/create", apiKeysHandler.CreateKey)
			adminRouter.Post("/api-keys/{id}/edit", apiKeysHandler.EditKey)
			adminRouter.Delete("/api-keys/{id}", apiKeysHandler.DeleteKey)

			// Models
			adminRouter.Get("/models", webModelsHandler.ShowModels)
			adminRouter.Post("/models/create", webModelsHandler.CreateModel)
			adminRouter.Post("/models/{id}/toggle", webModelsHandler.ToggleModel)
			adminRouter.Post("/models/{id}/default", webModelsHandler.SetDefaultModel)
			adminRouter.Delete("/models/{id}", webModelsHandler.DeleteModel)

			// Users & Groups
			adminRouter.Get("/users", usersHandler.ShowUsers)
			adminRouter.Post("/users/create", usersHandler.CreateUser)
			adminRouter.Post("/users/{id}/edit", usersHandler.EditUser)
			adminRouter.Delete("/users/{id}", usersHandler.DeleteUser)
			adminRouter.Post("/groups/create", usersHandler.CreateGroup)
			adminRouter.Post("/groups/{id}/edit", usersHandler.EditGroup)
			adminRouter.Delete("/groups/{id}", usersHandler.DeleteGroup)
		})
	})

	return r
}
