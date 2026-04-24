package domain

import "time"

type User struct {
	ID         int64      `json:"id"`
	LegacyID   *string    `json:"legacy_id,omitempty"`
	Username   string     `json:"username"`
	Nickname   string     `json:"nickname"`
	Password   string     `json:"-"`
	Email      string     `json:"email,omitempty"`
	Phone      string     `json:"phone,omitempty"`
	Avatar     string     `json:"avatar,omitempty"`
	IsAdmin    bool       `json:"is_admin"`
	Status     int        `json:"status"`
	Roles      []string   `json:"-"`
	MFAEnabled bool       `json:"mfa_enabled"`
	MFASecret  string     `json:"-"`
	CreatedAt  time.Time  `json:"create_time"`
	UpdatedAt  time.Time  `json:"update_time"`
	DeletedAt  *time.Time `json:"delete_time,omitempty"`
}

type ExternalIdentity struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Provider  string    `json:"provider"`
	Subject   string    `json:"subject"`
	Email     string    `json:"email,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
