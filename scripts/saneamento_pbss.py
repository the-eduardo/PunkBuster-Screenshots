#!/usr/bin/env python3
"""Saneamento idempotente do pbss.db: remove nomes vazios e GUIDs fantasma
(header do PunkBuster deslocado) de `players`/`player_names`.

Uso (rode direto no host onde o pbss.db vive):
    python3 scripts/saneamento_pbss.py            # dry-run, nao grava nada
    python3 scripts/saneamento_pbss.py --apply     # grava e faz backup antes

`--db PATH` sobrescreve o caminho do banco (default: producao). Existe so
para permitir testar o script contra uma copia sintetica sem editar o
arquivo — o uso normal em producao nunca precisa dele.

Criterio de "fantasma": guid que nao casa o padrao real gravado pelo parser
(internal/parser/pbheader.go:20, com ou sem asterisco), excluindo o sentinela
"unknown" que o pipeline grava para header vazio (internal/queue/pipeline.go).
Sem essa exclusao o script apagaria o sentinela, que so voltaria no proximo
screenshot vazio — inofensivo, mas gera ruido de log a toa.

A tabela `screenshots` nunca e tocada: as linhas com GUID fantasma continuam
sendo o registro de mensagens ja entregues ao Discord e seguem pesquisaveis
pelo nome. FK nao e' imposta (sqlite abre sem PRAGMA foreign_keys=ON), entao
apagar de `players`/`player_names` nao quebra `screenshots`.

O backup (`--apply`) fica no MESMO diretorio do banco, sem rotacao
automatica — o script nao apaga backups antigos sozinho. Ele e' reexecutavel
sob demanda (nao um cron), entao a limpeza de `*.bak-saneamento-*` velhos e'
manual: apague os que nao precisar mais depois de confirmar o resultado.
"""
import argparse
import os
import re
import sqlite3
import sys
import time

DB_PRODUCAO = "/home/ubuntu/projects/duck-PunkBuster-Screenshots-to-Discord/data/pbss.db"
GUID_OK = re.compile(r"^(\*[0-9a-fA-F]{32}\*|[0-9a-fA-F]{32})$")  # = parser/pbheader.go:20
SENTINELA = "unknown"  # = queue/pipeline.go


def eh_fantasma(guid):
    return guid != SENTINELA and not GUID_OK.match(guid)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="grava de verdade (default: dry-run)")
    parser.add_argument("--db", default=DB_PRODUCAO, help="caminho do pbss.db (default: producao)")
    args = parser.parse_args()

    src = sqlite3.connect(args.db, timeout=10, isolation_level=None)  # autocommit: transacao manual abaixo

    if args.apply:
        bak = "%s.bak-saneamento-%s" % (args.db, time.strftime("%Y%m%d%H%M%S"))
        with sqlite3.connect(bak) as dst:
            src.backup(dst)  # copia CONSISTENTE (inclui o WAL) — cp do .db sozinho nao serve
        os.chmod(bak, 0o600)  # copia completa de dados de jogadores: so o dono le
        print("backup:", bak)

    def conta(sql):
        return src.execute(sql).fetchone()[0]

    # shots_antes e fantasmas sao lidos DENTRO da transacao (achado do comite
    # de 12/09/2026, Dev Senior + QA convergentes): ler antes do BEGIN
    # IMMEDIATE deixa uma janela onde um INSERT concorrente do bot (que grava
    # o tempo todo em producao) muda a contagem sem a transacao do script ter
    # feito nada, disparando o abort de seguranca abaixo por falso-positivo.
    # BEGIN IMMEDIATE toma o lock de escrita imediatamente; a partir daqui o
    # snapshot e' o mesmo que a checagem final compara.
    src.execute("BEGIN IMMEDIATE")
    shots_antes = conta("SELECT count(*) FROM screenshots")
    fantasmas = [(g,) for (g,) in src.execute("SELECT guid FROM players") if eh_fantasma(g)]
    print(
        "antes: nomes vazios=%d  guids fantasma=%d"
        % (conta("SELECT count(*) FROM player_names WHERE name=''"), len(fantasmas))
    )

    n1 = src.execute("DELETE FROM player_names WHERE name=''").rowcount
    n2 = src.executemany("DELETE FROM player_names WHERE guid=?", fantasmas).rowcount
    n3 = src.executemany("DELETE FROM players WHERE guid=?", fantasmas).rowcount
    print(
        "apagados: player_names(name='')=%d  player_names(fantasma)=%d  players(fantasma)=%d"
        % (n1, n2, n3)
    )

    if conta("SELECT count(*) FROM screenshots") != shots_antes:
        src.execute("ROLLBACK")
        sys.exit("screenshots foi tocada — abortando sem gravar")

    print(
        "depois: nomes vazios=%d  players fantasma=%d"
        % (
            conta("SELECT count(*) FROM player_names WHERE name=''"),
            sum(1 for (g,) in src.execute("SELECT guid FROM players") if eh_fantasma(g)),
        )
    )

    if args.apply:
        src.execute("COMMIT")
        print("COMMIT")
    else:
        src.execute("ROLLBACK")
        print("dry-run: ROLLBACK (rode com --apply para gravar)")


if __name__ == "__main__":
    main()
