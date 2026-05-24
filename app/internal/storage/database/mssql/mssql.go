package mssql

import (
	"bus/app/internal/storage"
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/microsoft/go-mssqldb"
)

func Open(c *storage.Config) (*sqlx.DB, error) {

	connStr := fmt.Sprintf("%s://%s:%s@%s:%d?database=%s",
		c.DBType, c.User, c.Password, c.Host, c.Port, c.DBName)

	db, err := sqlx.Open(c.DBType, connStr)
	return db, err

}
