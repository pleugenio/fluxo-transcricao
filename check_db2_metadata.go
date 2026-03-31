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
		SELECT
			gravacao_id,
			nme_pessoa,
			nme_profissional,
			dsc_equipe,
			tpo_ligacao,
			dta_criacao,
			dta_discagem,
			dta_inicio_ligacao,
			dta_fim_ligacao,
			dsc_campanha,
			db2_metadata_json
		FROM transcricoes
		ORDER BY processado_em DESC
		LIMIT 10
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	fmt.Println("=== METADADOS DB2 SALVOS ===\n")

	count := 0
	for rows.Next() {
		var id, pessoa, prof, equipe, tipo, camp string
		var dtCriac, dtDiscagem, dtInicio, dtFim, metadata *string

		if err := rows.Scan(&id, &pessoa, &prof, &equipe, &tipo, &dtCriac, &dtDiscagem, &dtInicio, &dtFim, &camp, &metadata); err != nil {
			log.Fatal(err)
		}

		count++
		fmt.Printf("[%s]\n", id)
		fmt.Printf("  Pessoa: %v\n", pessoa)
		fmt.Printf("  Profissional: %v\n", prof)
		fmt.Printf("  Equipe: %v\n", equipe)
		fmt.Printf("  Tipo: %v\n", tipo)
		fmt.Printf("  Criação: %v\n", dtCriac)
		fmt.Printf("  Discagem: %v\n", dtDiscagem)
		fmt.Printf("  Início: %v\n", dtInicio)
		fmt.Printf("  Fim: %v\n", dtFim)
		fmt.Printf("  Campanha: %v\n", camp)
		fmt.Printf("  Metadata JSON: %v\n\n", metadata)
	}

	if count == 0 {
		fmt.Println("❌ NENHUM METADATA ENCONTRADO!")
		fmt.Println("\nIsso significa que fetch_db2.py não está sendo executado ou Python não está disponível.")
	} else {
		fmt.Printf("\n✓ %d registros com metadados encontrados\n", count)
	}
}
