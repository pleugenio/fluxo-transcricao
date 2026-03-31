package main

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	postgresURL := "postgres://srvbi:NbHo2WB8EyzatlPjmD1e@10.0.68.39:5433/transcriberdb"

	db, err := pgxpool.New(context.Background(), postgresURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(context.Background(), `
		SELECT gravacao_id, transcricao_txt
		FROM transcricoes
		ORDER BY processado_em DESC
		LIMIT 100
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	fmt.Println("=== TRANSCRIÇÕES ===\n")

	for rows.Next() {
		var id, txt string
		if err := rows.Scan(&id, &txt); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("[%s]\n%s\n\n", id, txt)
	}
}
