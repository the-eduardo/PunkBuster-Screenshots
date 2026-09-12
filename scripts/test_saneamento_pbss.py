"""Teste de fiacao para scripts/saneamento_pbss.py.

Nao existia teste algum para este script (drenagem de 12/09/2026, branch
auto/20260910-saneamento-pbss-db). Cobre, contra uma copia SINTETICA de banco
(nunca o pbss.db de producao):

  1. dry-run nao grava nada (nem cria backup, nem muda linha alguma);
  2. --apply remove nome vazio e guid fantasma de players/player_names;
  3. o sentinela "unknown" (queue/pipeline.go) NUNCA e' tratado como fantasma
     — e' a exclusao documentada no proprio script, e o ponto mais provavel
     de regressao silenciosa;
  4. a tabela screenshots nunca e' tocada (historico de mensagens ja
     entregues ao Discord tem que sobreviver, mesmo para guid fantasma);
  5. idempotencia: aplicar duas vezes seguidas produz o MESMO estado final
     que aplicar uma vez — a segunda rodada nao apaga nada a mais.
"""
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(REPO_ROOT, "scripts", "saneamento_pbss.py")

VALID_GUID = "a" * 32
FANTASMA_GUID = "GHOST_NAO_HEX_1234567890"
SENTINELA = "unknown"


def make_db(path):
    con = sqlite3.connect(path)
    con.executescript(
        """
        CREATE TABLE players (guid TEXT PRIMARY KEY);
        CREATE TABLE player_names (guid TEXT, name TEXT);
        CREATE TABLE screenshots (id INTEGER PRIMARY KEY, guid TEXT, name TEXT);
        """
    )
    con.executemany(
        "INSERT INTO players (guid) VALUES (?)",
        [(VALID_GUID,), (FANTASMA_GUID,), (SENTINELA,)],
    )
    con.executemany(
        "INSERT INTO player_names (guid, name) VALUES (?, ?)",
        [
            (VALID_GUID, "PlayerOne"),
            (VALID_GUID, ""),  # nome vazio a limpar, guid valido
            (FANTASMA_GUID, "GhostName"),
            (SENTINELA, "(sem GUID)"),
        ],
    )
    con.executemany(
        "INSERT INTO screenshots (id, guid, name) VALUES (?, ?, ?)",
        [
            (1, VALID_GUID, "shot1"),
            (2, FANTASMA_GUID, "shot2"),  # registro historico, deve sobreviver
            (3, SENTINELA, "shot3"),
        ],
    )
    con.commit()
    con.close()


def dump(path):
    con = sqlite3.connect(path)
    out = {}
    for table in ("players", "player_names", "screenshots"):
        out[table] = sorted(con.execute("SELECT * FROM %s" % table).fetchall())
    con.close()
    return out


def run_script(db_path, apply=False):
    args = [sys.executable, SCRIPT, "--db", db_path]
    if apply:
        args.append("--apply")
    return subprocess.run(args, capture_output=True, text=True, timeout=30)


class TestSaneamentoPbss(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="saneamento-test-")
        self.db = os.path.join(self.tmp, "pbss.db")
        make_db(self.db)

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_dry_run_nao_grava_nada(self):
        antes = dump(self.db)
        r = run_script(self.db, apply=False)
        self.assertEqual(r.returncode, 0, r.stderr)
        depois = dump(self.db)
        self.assertEqual(antes, depois, "dry-run alterou o banco")
        backups = [f for f in os.listdir(self.tmp) if "bak" in f]
        self.assertEqual(backups, [], "dry-run nao deveria criar backup")

    def test_apply_remove_fantasma_e_nome_vazio_preserva_sentinela(self):
        r = run_script(self.db, apply=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        d = dump(self.db)
        player_guids = {row[0] for row in d["players"]}
        self.assertNotIn(FANTASMA_GUID, player_guids, "guid fantasma deveria ter sido removido de players")
        self.assertIn(VALID_GUID, player_guids)
        self.assertIn(SENTINELA, player_guids, "sentinela 'unknown' NAO pode ser tratado como fantasma")

        names = d["player_names"]
        self.assertNotIn((VALID_GUID, ""), names, "nome vazio deveria ter sido removido")
        self.assertNotIn((FANTASMA_GUID, "GhostName"), names, "nome do guid fantasma deveria ter sido removido")
        self.assertIn((SENTINELA, "(sem GUID)"), names, "nome do sentinela deveria sobreviver")
        self.assertIn((VALID_GUID, "PlayerOne"), names)

    def test_backup_criado_com_permissao_600(self):
        r = run_script(self.db, apply=True)
        self.assertEqual(r.returncode, 0, r.stderr)
        backups = [f for f in os.listdir(self.tmp) if "bak-saneamento" in f]
        self.assertEqual(len(backups), 1, "esperava exatamente 1 backup")
        modo = os.stat(os.path.join(self.tmp, backups[0])).st_mode & 0o777
        self.assertEqual(
            modo, 0o600,
            "backup e' copia completa de dados de jogadores, deveria ser legivel so pelo dono (0600), veio %o" % modo,
        )

    def test_screenshots_nunca_e_tocada(self):
        antes = dump(self.db)["screenshots"]
        run_script(self.db, apply=True)
        depois = dump(self.db)["screenshots"]
        self.assertEqual(antes, depois, "screenshots foi alterada — script deveria tocar so players/player_names")

    def test_idempotente_segunda_aplicacao_nao_muda_nada(self):
        run_script(self.db, apply=True)
        estado_apos_1a = dump(self.db)
        r2 = run_script(self.db, apply=True)
        self.assertEqual(r2.returncode, 0, r2.stderr)
        estado_apos_2a = dump(self.db)
        self.assertEqual(
            estado_apos_1a, estado_apos_2a,
            "segunda aplicacao mudou o banco — script nao e' idempotente",
        )
        # segunda rodada nao deveria ter mais nada para apagar
        self.assertIn("antes: nomes vazios=0  guids fantasma=0", r2.stdout)


if __name__ == "__main__":
    unittest.main()
