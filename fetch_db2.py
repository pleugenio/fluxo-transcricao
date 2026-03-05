import ibm_db
import json
import sys

def fetch_metadata(gravacao_id):
    dsn = (
        "DRIVER={IBM DB2 ODBC DRIVER};"
        "DATABASE=DBPD01;"
        "HOSTNAME=brussels.srv.lbv.org.br;"
        "PORT=60001;"
        "PROTOCOL=TCPIP;"
        "UID=srvapnpbi;"
        "PWD=1oonSwtLMvzZ9tTRFP0X;"
    )

    try:
        conn = ibm_db.connect(dsn, "", "")
        if conn:
            sql = """
            SELECT 
                PE.NME_PESSOA, 
                pr.nme_profissional, 
                eq.dsc_equipe, 
                cla.TPO_LIGACAO, 
                cla.DTA_CRIACAO, 
                cla.DTA_DISCAGEM, 
                cla.DTA_INICIO_LIGACAO, 
                cla.DTA_FIM_LIGACAO, 
                ca.dsc_campanha
            FROM cct.CCT_LIGACAO_ATENDIDA cla
            INNER JOIN cct.CCT_GRV_GRAVACAO cgg ON cgg.CDG_CLIENTE = cla.SQC_LIGACAO_ATENDIDA
            INNER JOIN glb.glb_pessoa pe on cla.cdg_pessoa = pe.cdg_pessoa
            inner join apn.apn_profissional pr on pr.cdg_profissional = cla.cdg_profissional
            inner join apn.apn_equipe eq on eq.cdg_equipe = cla.cdg_equipe
            inner join cct.cct_campanha ca on ca.cdg_campanha = cla.cdg_campanha
            WHERE cgg.sqc_gravacao = ?
            """
            
            stmt = ibm_db.prepare(conn, sql)
            ibm_db.execute(stmt, (gravacao_id,))
            
            result = ibm_db.fetch_assoc(stmt)
            if result:
                # Converter objetos de data/hora para string para o JSON
                for key in result:
                    if hasattr(result[key], 'isoformat'):
                        result[key] = str(result[key])
                print(json.dumps(result))
            else:
                print(json.dumps({}))
                
            ibm_db.close(conn)
    except Exception as e:
        print(json.dumps({"error": str(e)}), file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Uso: python fetch_db2.py <id_gravacao>", file=sys.stderr)
        sys.exit(1)
    
    fetch_metadata(sys.argv[1])
