package server

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	v1 "mini-aigw/internal/api/v1"
	webapi "mini-aigw/internal/api/web"
	"mini-aigw/internal/config"
	"mini-aigw/internal/logging"
	"mini-aigw/internal/db"
	"mini-aigw/internal/eval"
	"mini-aigw/internal/services"
	"mini-aigw/internal/version"
	"mini-aigw/internal/views"
	"mini-aigw/web"
)

// Run は mini-aigw サーバーのメイン処理を実行します
func Run(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	versionFlag := fs.Bool("version", false, "Print version information and exit")
	vFlag := fs.Bool("v", false, "Print version information and exit (shorthand)")
	_ = fs.Parse(args)

	if *versionFlag || *vFlag {
		fmt.Printf("mini-aigw %s (commit: %s, built: %s)\n", version.Version, version.GitCommit, version.BuildTime)
		return
	}

	cfg := config.LoadConfig()

	log.Printf("Starting %s %s in environment (Debug=%v)...", cfg.ProjectName, version.Version, cfg.Debug)

	if err := logging.Init(cfg.ErrorLogFile); err != nil {
		log.Printf("Warning: error log file initialization failed (errors will only go to stderr): %v", err)
	} else {
		log.Printf("Error log file: %s", cfg.ErrorLogFile)
	}
	defer logging.Close()

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
	renderer, err := views.InitRenderer(cfg.BasePath)
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

	bedrockSvc := services.NewBedrockService(awsBundle, xberg, cfg, database)
	knowledgeSvc := services.NewKnowledgeService(awsBundle, database, cfg)
	ragPipeline := services.NewRAGPipeline(bedrockSvc, knowledgeSvc, cfg)
	evalSvc := eval.NewService(cfg, ragPipeline, bedrockSvc)

	oidcSvc, err := services.NewOIDCService(ctx, cfg)
	if err != nil {
		log.Printf("Warning: OIDC initialization error (OIDC login will be disabled): %v", err)
	}

	// 6. Build HTTP Router
	r := SetupRouter(cfg, database, renderer, bedrockSvc, knowledgeSvc, oidcSvc, evalSvc)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	writeTimeout := 300 * time.Second
	if cfg.ServerWriteTimeoutSec > 0 {
		writeTimeout = time.Duration(cfg.ServerWriteTimeoutSec) * time.Second
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("%s server listening on http://%s", cfg.ProjectName, addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server ListenAndServe error: %v", err)
		}
	}()

	<-stopChan
	log.Println("Shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
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
	evalSvc *eval.Service,
) chi.Router {
	if cfg != nil && renderer != nil && cfg.ProjectName != "" {
		renderer.SetProjectName(cfg.ProjectName)
	}
	chatHandler := v1.NewChatHandler(bedrockSvc, knowledgeSvc, cfg, database)
	modelsHandler := v1.NewModelsHandler(database, cfg)
	downloadHandler := v1.NewKnowledgeDownloadHandler(knowledgeSvc, database, cfg)

	authHandler := webapi.NewAuthHandler(database, cfg, oidcSvc, renderer)
	dashboardHandler := webapi.NewDashboardHandler(database, cfg, renderer)
	knowledgeHandler := webapi.NewKnowledgeHandler(knowledgeSvc, database, cfg, renderer)
	ragTestHandler := webapi.NewRAGTestHandler(bedrockSvc, knowledgeSvc, database, cfg, renderer)
	apiKeysHandler := webapi.NewAPIKeysHandler(database, cfg, renderer)
	webModelsHandler := webapi.NewWebModelsHandler(database, renderer, bedrockSvc)
	usersHandler := webapi.NewUsersHandler(database, renderer)
	zeroHitSvc := services.NewZeroHitService(database)
	zeroHitHandler := webapi.NewZeroHitHandler(zeroHitSvc, renderer)
	evalHandler := webapi.NewEvalHandler(evalSvc, database, cfg, renderer)

	r := chi.NewRouter()

	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(logging.Recoverer)
	r.Use(middleware.SessionAuthMiddleware(database, cfg))

	registerAppRoutes := func(sub chi.Router) {
		// Health check (ALB / EC2 AutoRecovery)
		sub.Get("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			resp := fmt.Sprintf(`{"status":"healthy","database":"ok","version":"%s"}`, version.Version)
			_, _ = w.Write([]byte(resp))
		})

		staticFS := http.FS(web.StaticFS())
		fileServer := http.FileServer(staticFS)
		sub.Handle("/static/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			idx := strings.Index(r.URL.Path, "/static/")
			if idx == -1 {
				http.NotFound(w, r)
				return
			}
			r2 := new(http.Request)
			*r2 = *r
			r2.URL = new(url.URL)
			*r2.URL = *r.URL
			r2.URL.Path = r.URL.Path[idx+len("/static/"):]
			fileServer.ServeHTTP(w, r2)
		}))
		sub.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
			data, err := web.Files.ReadFile("static/favicon.svg")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write(data)
		})

		// OpenAI-compatible v1 API (with CORS)
		sub.Route("/v1", func(v1Router chi.Router) {
			v1Router.Use(middleware.CORSMiddleware)
			v1Router.Get("/models", modelsHandler.ListModels)
			v1Router.Get("/knowledge/files/{doc_id}/download", downloadHandler.DownloadFile)
			v1Router.Get("/knowledge/download/{doc_id}", downloadHandler.DownloadFile)

			// API Key Protected
			v1Router.Group(func(authRouter chi.Router) {
				authRouter.Use(middleware.VerifyAPIKey(database))
				authRouter.Use(middleware.CheckAPIRateLimit(nil))
				authRouter.Post("/chat/completions", chatHandler.HandleChatCompletions)
			})
		})

		// Web UI Auth Routes
		sub.Get("/login", authHandler.ShowLogin)
		sub.With(middleware.SameOriginForUnsafeMethods).Post("/login", authHandler.HandleLocalLogin)
		sub.Get("/logout", authHandler.HandleLogout)
		sub.Get("/auth/oidc/login", authHandler.HandleOIDCLogin)
		sub.Get("/auth/oidc/callback", authHandler.HandleOIDCCallback)

		// Web UI Protected Routes (RequireAuth)
		sub.Group(func(webRouter chi.Router) {
			webRouter.Use(middleware.RequireAuth)
			webRouter.Use(middleware.SameOriginForUnsafeMethods)

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

			// Zero-Hit Queries
			webRouter.Get("/zero-hit-queries", zeroHitHandler.ShowZeroHitQueries)
			webRouter.Post("/zero-hit-queries/{id}/status", zeroHitHandler.HandleUpdateStatus)
			webRouter.Delete("/zero-hit-queries/{id}", zeroHitHandler.HandleDelete)
			webRouter.Get("/zero-hit-queries/export-csv", zeroHitHandler.HandleExportCSV)

			// Admin Only Routes
			webRouter.Group(func(adminRouter chi.Router) {
				adminRouter.Use(middleware.RequireAdmin)

				// Dashboard export
				adminRouter.Get("/dashboard/export-csv", dashboardHandler.ExportCSV)

				// API Keys
				adminRouter.Get("/api-keys", apiKeysHandler.ShowAPIKeys)
				adminRouter.Post("/api-keys/create", apiKeysHandler.CreateKey)
				adminRouter.Post("/api-keys/{id}/edit", apiKeysHandler.EditKey)
				adminRouter.Delete("/api-keys/{id}", apiKeysHandler.DeleteKey)

				// Models
				adminRouter.Get("/models", webModelsHandler.ShowModels)
				adminRouter.Post("/models/create", webModelsHandler.CreateModel)
				adminRouter.Post("/models/{id}/edit", webModelsHandler.EditModel)
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

				// RAG Evaluation Reports & Jobs
				adminRouter.Get("/eval-reports", evalHandler.ShowEvalReports)
				adminRouter.Post("/eval-reports/run", evalHandler.HandleRunEval)
				adminRouter.Get("/eval-reports/job-status", evalHandler.GetJobStatus)
				adminRouter.Get("/eval-reports/{id}", evalHandler.ShowReportDetail)
				adminRouter.Get("/eval-reports/{id}/raw", evalHandler.HandleRawReport)
				adminRouter.Get("/eval-reports/{id}/download", evalHandler.HandleDownloadReport)
				adminRouter.Delete("/eval-reports/{id}", evalHandler.HandleDeleteReport)
			})
		})
	}

	r.Group(registerAppRoutes)
	if cfg.BasePath != "" {
		r.Route(cfg.BasePath, registerAppRoutes)
	}

	return r
}

