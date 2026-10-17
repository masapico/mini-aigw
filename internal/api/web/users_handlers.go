package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"mini-aigw/internal/api/middleware"
	"mini-aigw/internal/auth"
	"mini-aigw/internal/models"
	"mini-aigw/internal/views"
)

type UsersHandler struct {
	db       *gorm.DB
	renderer *views.Renderer
}

func NewUsersHandler(db *gorm.DB, renderer *views.Renderer) *UsersHandler {
	return &UsersHandler{db: db, renderer: renderer}
}

func (h *UsersHandler) ShowUsers(w http.ResponseWriter, r *http.Request) {
	currentUser := middleware.GetUserFromContext(r.Context())
	if currentUser == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	var users []models.User
	h.db.Order("created_at asc").Find(&users)

	var groups []models.Group
	h.db.Order("name asc").Find(&groups)

	data := map[string]interface{}{
		"User":        currentUser,
		"CurrentUser": currentUser,
		"IsAdmin":     true,
		"CurrentPath": "/users",
		"Users":       users,
		"Groups":      groups,
	}

	_ = h.renderer.Render(w, "users.html", data)
}

func (h *UsersHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	currentUser := middleware.GetUserFromContext(r.Context())
	if currentUser == nil || !currentUser.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := strings.TrimSpace(r.FormValue("password"))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	var displayNamePtr *string
	if displayName != "" {
		displayNamePtr = &displayName
	}
	email := strings.TrimSpace(r.FormValue("email"))
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}

	if username == "" || password == "" {
		http.Error(w, "Username and password are required", http.StatusBadRequest)
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	roles := []string{"user"}
	if r.FormValue("is_admin") == "true" {
		roles = append(roles, "admin")
	}

	selectedGroups := r.Form["groups"]

	user := models.User{
		AuthType:     "local",
		Username:     username,
		PasswordHash: &hash,
		DisplayName:  displayNamePtr,
		Email:        emailPtr,
	}
	user.SetRoles(roles)
	user.SetGroups(selectedGroups)

	if err := h.db.Create(&user).Error; err != nil {
		http.Error(w, fmt.Sprintf("Failed to create user: %v", err), http.StatusBadRequest)
		return
	}

	var users []models.User
	h.db.Order("created_at asc").Find(&users)

	data := map[string]interface{}{
		"Users":       users,
		"CurrentUser": currentUser,
	}
	_ = h.renderer.RenderPartial(w, "users_table.html", data)
}

func (h *UsersHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	currentUser := middleware.GetUserFromContext(r.Context())
	if currentUser == nil || !currentUser.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	targetID := chi.URLParam(r, "id")
	if targetID == currentUser.ID {
		http.Error(w, "Cannot delete yourself", http.StatusBadRequest)
		return
	}

	if err := h.db.Delete(&models.User{}, "id = ?", targetID).Error; err != nil {
		http.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *UsersHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	currentUser := middleware.GetUserFromContext(r.Context())
	if currentUser == nil || !currentUser.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	desc := strings.TrimSpace(r.FormValue("description"))
	var descPtr *string
	if desc != "" {
		descPtr = &desc
	}

	if name == "" || displayName == "" {
		http.Error(w, "Name and display name are required", http.StatusBadRequest)
		return
	}

	g := models.Group{
		Name:        name,
		DisplayName: displayName,
		Description: descPtr,
	}
	if err := h.db.Create(&g).Error; err != nil {
		http.Error(w, fmt.Sprintf("Failed to create group: %v", err), http.StatusBadRequest)
		return
	}

	var groups []models.Group
	h.db.Order("name asc").Find(&groups)

	data := map[string]interface{}{
		"Groups": groups,
	}
	_ = h.renderer.RenderPartial(w, "groups_table.html", data)
}

func (h *UsersHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	currentUser := middleware.GetUserFromContext(r.Context())
	if currentUser == nil || !currentUser.HasRole("admin") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	groupID := chi.URLParam(r, "id")
	if err := h.db.Delete(&models.Group{}, "id = ?", groupID).Error; err != nil {
		http.Error(w, "Delete failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

