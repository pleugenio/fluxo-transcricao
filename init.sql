-- Criação da tabela principal de transcrições
CREATE TABLE IF NOT EXISTS transcricoes (
    id SERIAL PRIMARY KEY,
    gravacao_id VARCHAR(50) UNIQUE NOT NULL,
    processado_em TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    
    -- Transcrições
    transcricao_txt TEXT,
    transcricao_corrigida TEXT,
    timeline_json JSONB,
    atendente_json JSONB,
    cliente_json JSONB,
    
    -- Contadores
    total_turnos INTEGER,
    turnos_atendente INTEGER,
    turnos_cliente INTEGER,
    duracao_segundos NUMERIC(10,2),
    
    -- Qualidade de áudio
    snr_db NUMERIC(8,2),
    silence_ratio NUMERIC(5,3),
    clipping_ratio NUMERIC(8,5),
    dropout_count INTEGER,
    ch0_rms NUMERIC(8,4),
    ch1_rms NUMERIC(8,4),
    audio_enhanced BOOLEAN,
    diarizer VARCHAR(50),
    
    -- Metadados do DB2
    nme_pessoa VARCHAR(255),
    nme_profissional VARCHAR(255),
    dsc_equipe VARCHAR(255),
    tpo_ligacao VARCHAR(10),
    dta_criacao TIMESTAMP,
    dta_discagem TIMESTAMP,
    dta_inicio_ligacao TIMESTAMP,
    dta_fim_ligacao TIMESTAMP,
    dsc_campanha TEXT,
    db2_metadata_json JSONB
);

-- Índices para performance
CREATE INDEX IF NOT EXISTS idx_transcricoes_gravacao_id ON transcricoes(gravacao_id);
CREATE INDEX IF NOT EXISTS idx_transcricoes_processado_em ON transcricoes(processado_em DESC);
CREATE INDEX IF NOT EXISTS idx_transcricoes_nme_pessoa ON transcricoes(nme_pessoa);

-- Tabela de logs (opcional, para monitoramento)
CREATE TABLE IF NOT EXISTS processing_logs (
    id SERIAL PRIMARY KEY,
    gravacao_id VARCHAR(50),
    stage VARCHAR(50),
    status VARCHAR(50),
    message TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_processing_logs_gravacao_id ON processing_logs(gravacao_id);
