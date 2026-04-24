package application

import (
	"context"
	"testing"

	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
)

func TestResolveUserPrefersLegacyIDBeforeNumericPrimaryKey(t *testing.T) {
	t.Parallel()

	legacyID := "42"
	userRepo := &memoryUserRepository{
		usersByID: map[int64]domain.User{
			42: {ID: 42, Username: "new-id-user", Nickname: "New ID User", Status: USER_STATUS_ACTIVE},
			99: {ID: 99, LegacyID: &legacyID, Username: "legacy-user", Nickname: "Legacy User", Status: USER_STATUS_ACTIVE},
		},
	}
	service := NewService(userRepo, validator.New(), nil)

	user, err := service.resolveUser(context.Background(), "42")
	if err != nil {
		t.Fatalf("resolve user by legacy id: %v", err)
	}
	if user.ID != 99 {
		t.Fatalf("expected legacy-id user 99, got %d", user.ID)
	}
}

func TestCreateAndUpdatePreserveIsAdminFlag(t *testing.T) {
	t.Parallel()

	userRepo := &memoryUserRepository{
		usersByID: map[int64]domain.User{
			1: {
				ID:       1,
				Username: "origin",
				Nickname: "Origin",
				Status:   USER_STATUS_ACTIVE,
				Roles:    []string{"user"},
			},
		},
		nextID: 2,
	}
	service := NewService(userRepo, validator.New(), nil)

	isAdmin := true
	created, err := service.Create(context.Background(), CreateUserRequest{
		Username: "superman",
		Nickname: "Super Man",
		Password: "password-123",
		IsAdmin:  &isAdmin,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if !created.IsAdmin {
		t.Fatal("expected created user to be admin")
	}

	isAdmin = false
	updated, err := service.Update(context.Background(), "1", UpdateUserRequest{
		Username: "origin",
		Nickname: "Origin Updated",
		Email:    "",
		Phone:    "",
		Avatar:   "",
		Status:   intPtr(USER_STATUS_ACTIVE),
		IsAdmin:  &isAdmin,
	})
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if updated.IsAdmin {
		t.Fatal("expected updated user admin flag to be false")
	}
}

type memoryUserRepository struct {
	usersByID map[int64]domain.User
	nextID    int64
}

func (r *memoryUserRepository) List(context.Context) ([]domain.User, error) {
	out := make([]domain.User, 0, len(r.usersByID))
	for _, user := range r.usersByID {
		out = append(out, user)
	}
	return out, nil
}

func (r *memoryUserRepository) Get(_ context.Context, id int64) (domain.User, error) {
	user, ok := r.usersByID[id]
	if !ok {
		return domain.User{}, gorm.ErrRecordNotFound
	}
	return user, nil
}

func (r *memoryUserRepository) GetByLegacyID(_ context.Context, legacyID string) (domain.User, error) {
	for _, user := range r.usersByID {
		if user.LegacyID != nil && *user.LegacyID == legacyID {
			return user, nil
		}
	}
	return domain.User{}, gorm.ErrRecordNotFound
}

func (r *memoryUserRepository) GetByUsername(_ context.Context, username string) (domain.User, error) {
	for _, user := range r.usersByID {
		if user.Username == username {
			return user, nil
		}
	}
	return domain.User{}, gorm.ErrRecordNotFound
}

func (r *memoryUserRepository) Create(_ context.Context, user domain.User) (domain.User, error) {
	if user.ID == 0 {
		user.ID = r.nextID
		r.nextID++
	}
	r.usersByID[user.ID] = user
	return user, nil
}

func (r *memoryUserRepository) Update(_ context.Context, user domain.User) (domain.User, error) {
	if _, ok := r.usersByID[user.ID]; !ok {
		return domain.User{}, gorm.ErrRecordNotFound
	}
	r.usersByID[user.ID] = user
	return user, nil
}

func (r *memoryUserRepository) Delete(_ context.Context, id int64) error {
	if _, ok := r.usersByID[id]; !ok {
		return gorm.ErrRecordNotFound
	}
	delete(r.usersByID, id)
	return nil
}

func (r *memoryUserRepository) GetExternalIdentity(context.Context, string, string) (domain.ExternalIdentity, error) {
	return domain.ExternalIdentity{}, gorm.ErrRecordNotFound
}

func (r *memoryUserRepository) CreateExternalIdentity(context.Context, domain.ExternalIdentity) error {
	return nil
}

func (r *memoryUserRepository) CreateWithExternalIdentity(_ context.Context, user domain.User, _ domain.ExternalIdentity) (domain.User, error) {
	return r.Create(context.Background(), user)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func intPtr(value int) *int {
	return &value
}
