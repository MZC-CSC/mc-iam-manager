package models

import (
	"log"
	"time"

	"github.com/gobuffalo/envy"
	"github.com/gobuffalo/pop/v6"
	"github.com/gofrs/uuid"
)

// DB is a connection to your database to be used
// throughout your application.
var DB *pop.Connection

func init() {
	var err error
	env := envy.Get("GO_ENV", "development")
	DB, err = pop.Connect(env)
	if err != nil {
		log.Fatal(err)
	}
	pop.Debug = env == "development"
}

type User struct {
	ID               uuid.UUID `json:"id" db:"id"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
	UpdatedAt        time.Time `json:"updated_at" db:"updated_at"`
	Email            string    `json:"email" db:"email"`
	PasswordHash     string    `json:"password_hash" db:"password_hash"`
	DefaultNamespace string    `json:"default_namespace" db:"default_namespace"`
	// DefaultNsName        string         `json:"default_ns_name" db:"-"`
}
