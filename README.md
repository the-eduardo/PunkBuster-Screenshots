# PunkBuster-Screenshots-to-Discord (duck-pbss)

Bot em Go que monitora um servidor FTP/sFTP de PunkBuster (ex.: BF4), envia cada
screenshot capturado para um canal do Discord com o nome do jogador e o PBGUID,
e mantém um índice pesquisável (SQLite) consultável direto no Discord via slash
commands (`/pbss search`, `/pbss last`, `/pbss stats`).

O Discord é o armazenamento permanente das imagens (comprovado em anos de uso).
O disco local é só uma fila de trânsito temporária entre o download e a
confirmação de envio — nada fica retido localmente por mais de `RETENTION_HOURS`.

## Arquitetura

```
cmd/bot/main.go            bootstrap: config, sqlite, sessão discord, pipeline
internal/config            leitura/validação de variáveis de ambiente
internal/source            abstração FTP/sFTP com reconexão automática
internal/parser            extração robusta do GUID+nome do cabeçalho do screenshot
internal/storage           índice SQLite (players, player_names, screenshots)
internal/queue             pipeline: baixar -> enfileirar -> confirmar -> limpar
internal/discord           sessão persistente + fila de envio com retry
internal/discord/commands  slash commands (/pbss search|last|stats)
```

Ciclo de vida de cada screenshot: baixa pro disco local → extrai GUID/nome →
enfileira envio ao Discord → **só após confirmação** grava no índice e apaga o
arquivo local e o remoto. Se o envio falhar, nada é apagado (retry no próximo
ciclo). Um "janitor" força a limpeza de arquivos locais mais antigos que
`RETENTION_HOURS`, mesmo sem confirmação, evitando acúmulo de disco.

## Pré-requisitos

- Docker e Docker Compose
- Um Bot do Discord (token no [Developer Portal](https://discord.com/developers/applications))

## Uso

1. Clone este repositório e copie o arquivo de exemplo de configuração:

```bash
git clone https://github.com/pruu-networking/PunkBuster-Screenshots-to-Discord duck-pbss
cd duck-pbss
cp .env.example .env
```

2. Edite o `.env` (nunca é commitado) com os dados reais do servidor e do bot —
   veja os comentários em [.env.example](.env.example).

3. Suba com docker-compose:

```bash
sudo docker compose up --build -d
```

## Atualizando para uma nova versão

```bash
git pull
sudo docker compose up -d --build
```

O SQLite e a fila temporária ficam no volume `./data`, então nada se perde
entre atualizações.

## Slash commands

- `/pbss search termo:<nome ou GUID>` — busca paginada (botões ◀ ▶), com link
  direto pra mensagem original no Discord.
- `/pbss last termo:<nome ou GUID> quantidade:<N>` — atalho pros N mais recentes.
- `/pbss stats` — total de screenshots, jogadores distintos flagrados e top 10.

Por padrão os comandos são registrados globalmente (demora até ~1h pra
propagar). Defina `DISCORD_GUILD_ID` no `.env` para propagar instantaneamente
num servidor específico (útil em testes).

## Variáveis de ambiente

Ver [.env.example](.env.example) para a lista completa e os valores padrão.
