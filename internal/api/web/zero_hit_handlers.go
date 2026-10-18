package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/services"
	"mini-aigw/internal/views"
)

type ZeroHitHandler struct {
	zeroHitService *services.ZeroHitService
	renderer       *views.Renderer
}

func NewZeroHitHandler(zeroHitService *services.ZeroHitService, renderer *views.Renderer) *ZeroHitHandler {
	return &ZeroHitHandler{
		zeroHitService: zeroHitService,
		renderer:       renderer,
	}
}

// ShowZeroHitQueries は未解決クエリ分析画面を表示します
func (h *ZeroHitHandler) ShowZeroHitQueries(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	isAdmin := user.HasRole("admin")
	var allowedGroups []string
	if !isAdmin {
		allowedGroups = append(user.GetGroups(), "public")
	}

	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter == "" {
		statusFilter = "unresolved"
	}
	searchKeyword := strings.TrimSpace(r.URL.Query().Get("search"))

	queries, err := h.zeroHitService.ListQueries(allowedGroups, statusFilter, searchKeyword)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list queries: %v", err), http.StatusInternalServerError)
		return
	}

	unresolvedCount, _ := h.zeroHitService.GetUnresolvedCount(allowedGroups)

	data := map[string]interface{}{
		"User":            user,
		"IsAdmin":         isAdmin,
		"CurrentPath":     "/zero-hit-queries",
		"Queries":         queries,
		"StatusFilter":    statusFilter,
		"SearchKeyword":   searchKeyword,
		"UnresolvedCount": unresolvedCount,
	}

	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "zero-hit-table-container" {
		_ = h.renderer.RenderPartial(w, "zero_hit_table.html", data)
		return
	}

	_ = h.renderer.Render(w, "zero_hit_queries.html", data)
}

// HandleUpdateStatus はクエリのステータス（resolved / ignored / unresolved）を変更します
func (h *ZeroHitHandler) HandleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "Missing query id", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	targetStatus := strings.TrimSpace(r.FormValue("status"))
	if targetStatus == "" {
		targetStatus = "resolved"
	}

	_, err := h.zeroHitService.UpdateStatus(id, targetStatus)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to update status: %v", err), http.StatusInternalServerError)
		return
	}

	// HTMX で該当の行を消し込む (未解決一覧表示時) または一覧を再描画
	currentFilter := strings.TrimSpace(r.FormValue("current_filter"))
	if currentFilter == "" || currentFilter == "unresolved" {
		// 行を画面から消去するため空文字返却
		w.WriteHeader(http.StatusOK)
		return
	}

	// フィルタが all / resolved / ignored の場合は再取得してテーブル更新
	isAdmin := user.HasRole("admin")
	var allowedGroups []string
	if !isAdmin {
		allowedGroups = append(user.GetGroups(), "public")
	}
	queries, _ := h.zeroHitService.ListQueries(allowedGroups, currentFilter, "")
	unresolvedCount, _ := h.zeroHitService.GetUnresolvedCount(allowedGroups)
	data := map[string]interface{}{
		"User":            user,
		"IsAdmin":         isAdmin,
		"Queries":         queries,
		"StatusFilter":    currentFilter,
		"UnresolvedCount": unresolvedCount,
	}
	_ = h.renderer.RenderPartial(w, "zero_hit_table.html", data)
}

// HandleDelete は誤入力されたクエリを物理削除します
func (h *ZeroHitHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "Missing query id", http.StatusBadRequest)
		return
	}

	if err := h.zeroHitService.Delete(id); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete query: %v", err), http.StatusInternalServerError)
		return
	}

	// HTMX で行自体を削除 (hx-swap="delete")
	w.WriteHeader(http.StatusOK)
}

// HandleExportCSV は未解決クエリ一覧を CSV 形式でダウンロードします
func (h *ZeroHitHandler) HandleExportCSV(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	isAdmin := user.HasRole("admin")
	var allowedGroups []string
	if !isAdmin {
		allowedGroups = append(user.GetGroups(), "public")
	}

	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter == "" {
		statusFilter = "all"
	}

	filename := fmt.Sprintf("zero_hit_queries_%s.csv", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	_ = h.zeroHitService.ExportCSV(allowedGroups, statusFilter, w)
}
