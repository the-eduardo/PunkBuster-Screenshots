// Package storage mantém o índice SQLite de screenshots (GUID, nome, referência
// da mensagem no Discord). O Discord é o armazenamento permanente das imagens;
// este banco é só o índice de busca.
package storage

import (
	"database/sql"
	"embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("falha ao abrir sqlite em %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // sqlite: evita "database is locked" sob escrita concorrente

	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		sqlBytes, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("falha ao ler migration %s: %w", e.Name(), err)
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return nil, fmt.Errorf("falha ao aplicar migration %s: %w", e.Name(), err)
		}
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
